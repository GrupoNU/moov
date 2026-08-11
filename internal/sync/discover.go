package sync

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// syncMailbox pairs a stored mailbox row with what the server just said about
// it, so later phases do not have to re-list or re-look-up either half.
type syncMailbox struct {
	row  store.Mailbox
	info imap.MailboxInfo
}

// isInbox reports whether this is the account's INBOX, which is the mailbox
// phase A covers.
//
// The role is authoritative when the server reports SPECIAL-USE; the name check
// is the fallback for a server that does not, and it is safe because "INBOX" is
// case-insensitively reserved by RFC 3501 §5.1 — no other folder can be called
// that.
func (m syncMailbox) isInbox() bool {
	return m.row.Role == store.RoleInbox || equalFoldASCII(m.info.Name, "INBOX")
}

// discover lists the account's mailboxes and upserts them, returning the
// selectable ones in the order the sync should visit them.
//
// It always runs, even on a resumed account: a folder created since the last
// run must be picked up, and UpsertMailbox deliberately leaves the sync state
// alone (see its doc), so re-listing costs nothing and loses nothing.
func (s *Syncer) discover(ctx context.Context, account store.Account, log *slog.Logger) ([]syncMailbox, error) {
	var infos []imap.MailboxInfo
	err := s.conns.withConn(ctx, func(c imap.Client) error {
		var err error
		infos, err = c.ListMailboxes(ctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("listing mailboxes: %w", err)
	}

	out := make([]syncMailbox, 0, len(infos))
	for _, info := range infos {
		if info.NoSelect {
			// A parent node that exists only to hold children. Selecting it is
			// a protocol error, so it is not a mailbox to sync — but it is
			// still stored, because the JMAP layer renders the folder tree and
			// a missing intermediate node breaks the hierarchy.
			if _, err := s.store.UpsertMailbox(ctx, mailboxRow(account.ID, info)); err != nil {
				return nil, fmt.Errorf("upserting mailbox %q: %w", info.Name, err)
			}
			continue
		}

		row, err := s.store.UpsertMailbox(ctx, mailboxRow(account.ID, info))
		if err != nil {
			return nil, fmt.Errorf("upserting mailbox %q: %w", info.Name, err)
		}
		out = append(out, syncMailbox{row: row, info: info})
	}

	// INBOX first, then the rest by name. The order is not cosmetic: phase B
	// processes mailboxes in sequence, and the inbox holds the mail a user
	// looks for, so its history should be complete before an archive folder's.
	sort.SliceStable(out, func(i, j int) bool {
		switch {
		case out[i].isInbox() != out[j].isInbox():
			return out[i].isInbox()
		default:
			return out[i].info.Name < out[j].info.Name
		}
	})

	log.Info("mailboxes discovered", "count", len(out), "listed", len(infos))
	return out, nil
}

// mailboxRow converts what LIST reported into the store's row shape.
//
// The sync-state columns (uidvalidity, uidnext, highestmodseq, backfill_*) are
// intentionally left zero: UpsertMailbox does not write them, and the values
// LIST-STATUS reports are a snapshot from before the mailbox was selected. The
// authoritative ones come from SELECT and are written by SetMailboxSyncState.
func mailboxRow(accountID int64, info imap.MailboxInfo) store.Mailbox {
	delim := info.Delimiter
	if delim == "" {
		delim = "/"
	}
	return store.Mailbox{
		AccountID:  accountID,
		Name:       info.Name,
		Delimiter:  delim,
		Role:       storeRole(info.Role),
		Subscribed: info.Subscribed,
		Selectable: !info.NoSelect,
	}
}

// storeRole maps the IMAP package's role vocabulary onto the store's.
//
// The two enumerations are identical today and the mapping is a one-liner in
// every arm — which is exactly why it is written out rather than cast. They are
// separate types owned by separate packages, and a silent conversion would make
// a future divergence (a role one side learns about and the other does not) a
// wrong value in the database instead of a compile error here.
func storeRole(r imap.MailboxRole) store.MailboxRole {
	switch r {
	case imap.RoleInbox:
		return store.RoleInbox
	case imap.RoleArchive:
		return store.RoleArchive
	case imap.RoleDrafts:
		return store.RoleDrafts
	case imap.RoleSent:
		return store.RoleSent
	case imap.RoleJunk:
		return store.RoleJunk
	case imap.RoleTrash:
		return store.RoleTrash
	case imap.RoleAll:
		return store.RoleAll
	case imap.RoleFlagged:
		return store.RoleFlagged
	case imap.RoleNone:
		return store.RoleNone
	default:
		return store.RoleNone
	}
}

// equalFoldASCII is strings.EqualFold restricted to ASCII, which is what
// RFC 3501's case-insensitive "INBOX" comparison means. The Unicode-aware
// version would additionally fold characters that cannot appear in the literal
// being matched, so the narrower rule is the correct one.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
