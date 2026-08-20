package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/parser"
	"github.com/GrupoNU/moov/internal/store"
)

// The APPEND half of the write executor (W3, L2-jmap-write §3): Email/set
// create appends assembled drafts, and the outbox appends the transmitted copy
// to \Sent after the SMTP 250 (ADR §4).
//
// # W-A1, applied to a create
//
// Dovecot first, store second, answer last — the same ordering write.go states
// for updates, with the reflection inverted: instead of re-pointing an
// existing row, a create INSERTS the row the ordinary sync would have created,
// at the (mailbox, uidvalidity, uid) the server's own [APPENDUID] names. The
// echo-safety argument is the MOVE one verbatim: the watcher's NOTIFY for the
// appended message triggers an incremental pass, the pass finds the UID
// already in ExistingUIDs, and never re-fetches the body. Convergent from both
// directions, and InsertMessages' ON CONFLICT DO NOTHING closes the race for
// the pass that arrives between the APPEND and the reflection.
//
// # Why UIDPLUS is required up front
//
// Without [APPENDUID] the executor cannot know which UID the message landed
// on, so it cannot reflect the create, so Email/set create cannot return the
// id RFC 8620 §5.3 requires — and failing AFTER the append leaves a message on
// the server that a client retry would duplicate. Refusing BEFORE the append
// is the only ordering with no duplicate window. Dovecot always has UIDPLUS;
// the refusal exists for the protocol's sake, not for our deployment's.

// ErrAppendNotSupported means the server offers no UIDPLUS [APPENDUID], so an
// append could not be reflected locally and was refused before anything was
// written (see the package note above).
var ErrAppendNotSupported = errors.New("sync: the server lacks UIDPLUS; an append could not be reflected and was refused")

// AppendBlobStore is what ApplyAppend needs from the blob layer: durable bytes
// plus the reference that keeps them alive. *blob.Store satisfies it.
type AppendBlobStore interface {
	Put(ctx context.Context, r io.Reader) (blob.Hash, int64, error)
	AddRefTx(ctx context.Context, h blob.Hash, accountID int64, kind blob.OwnerKind, ownerID int64) error
}

// AppendedMessage reports a reflected append.
type AppendedMessage struct {
	// MessageID is the store id of the new row — the JMAP Email id.
	MessageID int64
	// ThreadID is the thread the message was assigned to.
	ThreadID int64
	// BlobHash addresses the raw bytes; Size is their length.
	BlobHash blob.Hash
	Size     int64
	// UID is where the message landed on the server.
	UID int64
}

// ApplyAppend appends raw RFC 5322 bytes to one of the account's mailboxes and
// reflects the result into the store, per the ordering above.
//
// flags use the imap package's normalized vocabulary (bare system flag names,
// user keywords verbatim) — the same one the rest of the executor speaks.
func (w *WriteExecutor) ApplyAppend(ctx context.Context, accountID, mailboxID int64, raw []byte, flags []string) (AppendedMessage, error) {
	var out AppendedMessage

	if w.blobs == nil {
		return out, errors.New("sync: the write executor has no blob store; appends are not wired")
	}
	if len(raw) == 0 {
		return out, errors.New("sync: refusing to append an empty message")
	}

	mb, err := w.store.GetMailbox(ctx, mailboxID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, ErrWriteNotFound
		}
		return out, fmt.Errorf("loading mailbox %d: %w", mailboxID, err)
	}
	if mb.AccountID != accountID || !mb.Selectable {
		return out, ErrWriteNotFound
	}

	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return out, fmt.Errorf("loading account %d: %w", accountID, err)
	}

	err = w.withMailbox(ctx, account, mb.Name, func(c imap.Client, sel imap.SelectResult) error {
		// The refusal happens with nothing yet written — see the package note.
		if !c.Capabilities().Has(imap.CapUIDPlus) {
			return ErrAppendNotSupported
		}

		res, err := c.Append(ctx, mb.Name, raw, flags, time.Now())
		if err != nil {
			return fmt.Errorf("appending to %q: %w", mb.Name, err)
		}
		if res.UID == 0 {
			// UIDPLUS was advertised but no [APPENDUID] arrived — a server this
			// engine has never met. The message IS on the server; the honest
			// report is a loud error naming the consequence, and the ordinary
			// sync will discover the message. A caller that retries may
			// duplicate, which is why this is an error and not a silent
			// degradation.
			return fmt.Errorf("appending to %q: the server sent no APPENDUID; the message will appear via sync", mb.Name)
		}

		uidValidity := res.UIDValidity
		if uidValidity == 0 {
			uidValidity = sel.UIDValidity
		}

		// Read back the appended message for the server's own flag
		// normalization and its modseq — the applies-and-reflects half of
		// W-A1, exactly as write.go's flag path does after a STORE.
		msg, err := fetchOne(ctx, c, res.UID)
		if err != nil {
			return err
		}
		if msg == nil {
			// Expunged between the APPEND and the read-back (another client,
			// a server-side filter). The append happened and the message is
			// gone; reflect nothing and report the truth.
			return fmt.Errorf("appended message uid %d disappeared before it could be reflected", res.UID)
		}

		reflected, err := w.reflectAppend(ctx, account.ID, mb.ID, uidValidity, raw, msg)
		if err != nil {
			return err
		}
		out = reflected
		return nil
	})
	if err == nil {
		w.broker.Notify(accountID)
	}
	return out, err
}

// reflectAppend stores the appended message: blob, rows, references, thread.
//
// It mirrors the sync pipeline's commit step (pipeline.go commitBatch) at
// batch size one, reusing the same conversion helpers so the row a create
// writes is byte-for-byte the row the ordinary sync would have written for the
// same bytes — which is what makes the two paths convergent rather than
// merely similar.
func (w *WriteExecutor) reflectAppend(ctx context.Context, accountID, mailboxID int64, uidValidity uint32, raw []byte, msg *imap.Message) (AppendedMessage, error) {
	var out AppendedMessage

	hash, size, err := w.blobs.Put(ctx, bytes.NewReader(raw))
	if err != nil {
		return out, fmt.Errorf("storing blob for appended message: %w", err)
	}

	parsed := parser.Parse(bytes.NewReader(raw), parser.Limits{})

	pm := parsedMessage{
		raw: rawMessage{
			uid:          msg.UID,
			modSeq:       msg.ModSeq,
			flags:        storeFlags(msg.Flags),
			keywords:     msg.Keywords,
			internalDate: msg.InternalDate,
			hash:         hash,
			size:         size,
		},
		parsed: parsed,
	}

	// newMessage is a Syncer method only because it reads the Syncer's clock;
	// a throwaway carrier with defaulted options gives it one without pulling
	// a full Syncer into the write executor.
	carrier := &Syncer{opts: Options{Logger: w.log}.withDefaults()}
	row := carrier.newMessage(accountID, mailboxID, uidValidity, &pm)

	ids, err := w.store.InsertMessages(ctx, []store.NewMessage{row})
	if err != nil {
		return out, fmt.Errorf("reflecting appended message in the store: %w", err)
	}
	if len(ids) == 0 || ids[0] == 0 {
		// Already present: the watcher's echo pass won the race, which the
		// package note calls out as expected. The row exists; find it.
		st, err := w.store.GetMessageStateByUID(ctx, mailboxID, int64(uidValidity), int64(msg.UID))
		if err != nil {
			return out, fmt.Errorf("resolving raced append reflection: %w", err)
		}
		m, err := w.store.GetMessage(ctx, st.MessageID)
		if err != nil {
			return out, fmt.Errorf("resolving raced append reflection: %w", err)
		}
		return AppendedMessage{MessageID: m.ID, ThreadID: m.ThreadID, BlobHash: hash, Size: size, UID: int64(msg.UID)}, nil
	}
	id := ids[0]

	if err := w.blobs.AddRefTx(ctx, hash, accountID, blob.OwnerMessage, id); err != nil {
		return out, fmt.Errorf("referencing blob for appended message: %w", err)
	}

	threadID := id
	assignments, err := w.store.AssignThreads(ctx, accountID, []int64{id}, []store.ThreadCandidate{threadCandidate(&row.Message)})
	if err != nil {
		// Same stance as the pipeline: the message is committed and each
		// message is validly its own thread until the reindex converges it.
		// Threading failure must not fail a create that already happened.
		w.log.Warn("threading an appended message failed; it remains a single-message thread",
			"account_id", accountID, "message_id", id, "error", err)
	} else if len(assignments) == 1 {
		threadID = assignments[0].ThreadID
	}

	return AppendedMessage{MessageID: id, ThreadID: threadID, BlobHash: hash, Size: size, UID: int64(msg.UID)}, nil
}

// ---------------------------------------------------------------------------
// the \Sent copy (outbox post-send step)
// ---------------------------------------------------------------------------

// SentContainsMessageID reports whether the account's \Sent mailbox already
// holds a live message with the given RFC 5322 Message-ID — the dedupe
// question of ADR §4, asked before ever appending a copy and again during
// crash recovery.
//
// It reads Moov's own store (store.MailboxContainsMessageID), which is exact
// for every copy this executor put there and for the draft the §7.5
// onSuccessUpdateEmail moved there (both reflect synchronously), and lags only
// by the watcher's latency for copies OTHER software delivered — a
// sieve-copied message, a copy from another client. internal/submit documents
// the consequence: the worst outcome of that lag is a duplicate \Sent COPY,
// never a duplicate send.
func (w *WriteExecutor) SentContainsMessageID(ctx context.Context, accountID int64, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	sent, err := w.store.GetMailboxByRole(ctx, accountID, store.RoleSent)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("resolving the Sent mailbox: %w", err)
	}
	return w.store.MailboxContainsMessageID(ctx, sent.ID, messageID)
}

// AppendToSent appends the transmitted bytes to the account's \Sent mailbox,
// unless a message with the same Message-ID is already there — in which case
// it reports deduped=true and appends nothing (ADR §4: "dedupe por Message-ID
// rather than double-appending").
//
// The appended copy carries \Seen: it is the user's own sent mail, and every
// client the Gmail-class bar points at stores it read.
func (w *WriteExecutor) AppendToSent(ctx context.Context, accountID int64, raw []byte, messageID string) (deduped bool, err error) {
	dup, err := w.SentContainsMessageID(ctx, accountID, messageID)
	if err != nil {
		return false, err
	}
	if dup {
		return true, nil
	}

	sent, err := w.store.GetMailboxByRole(ctx, accountID, store.RoleSent)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, fmt.Errorf("the account has no Sent mailbox: %w", ErrWriteNotFound)
		}
		return false, fmt.Errorf("resolving the Sent mailbox: %w", err)
	}

	if _, err := w.ApplyAppend(ctx, accountID, sent.ID, raw, []string{"seen"}); err != nil {
		return false, err
	}
	return false, nil
}
