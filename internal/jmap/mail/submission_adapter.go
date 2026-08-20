package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/store"
	"github.com/GrupoNU/moov/internal/submit"
)

// The store-backed SubmissionStore: the only file in the JMAP surface that
// knows EmailSubmission objects are intent rows — the same confinement
// adapter.go gives the readers and write_adapter.go the writers.

// SubmissionNotifier is told when the submission data of an account changed —
// *sync.Broker through cmd/moovd, so an enqueue/cancel/destroy pushes an SSE
// StateChange exactly like a flag write does. nil is a valid, quiet wiring.
type SubmissionNotifier interface {
	Notify(accountID int64)
}

// SubmissionAdapter implements SubmissionStore over the real store.
type SubmissionAdapter struct {
	store    *store.Store
	notifier SubmissionNotifier
}

// NewSubmissionAdapter builds the adapter. notifier may be nil.
func NewSubmissionAdapter(st *store.Store, notifier SubmissionNotifier) (*SubmissionAdapter, error) {
	if st == nil {
		return nil, errors.New("mail: a store is required")
	}
	return &SubmissionAdapter{store: st, notifier: notifier}, nil
}

var _ SubmissionStore = (*SubmissionAdapter)(nil)

// SubmissionsByID implements SubmissionStore.
func (a *SubmissionAdapter) SubmissionsByID(ctx context.Context, accountID int64, ids []int64) ([]SubmissionRow, error) {
	intents, err := a.store.SendIntentsByID(ctx, accountID, ids)
	if err != nil {
		return nil, err
	}
	return submissionRows(intents), nil
}

// ListSubmissions implements SubmissionStore.
func (a *SubmissionAdapter) ListSubmissions(ctx context.Context, accountID int64, limit int) ([]SubmissionRow, error) {
	intents, err := a.store.ListSendIntents(ctx, accountID, limit)
	if err != nil {
		return nil, err
	}
	return submissionRows(intents), nil
}

// SubmissionState implements SubmissionStore — the same watermark-and-count
// grammar every other state string uses, over the send intents.
func (a *SubmissionAdapter) SubmissionState(ctx context.Context, accountID int64) (string, error) {
	return submissionStateString(ctx, a.store, accountID)
}

// SubmissionsChangedSince implements SubmissionStore.
func (a *SubmissionAdapter) SubmissionsChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]SubmissionRow, error) {
	intents, err := a.store.SendIntentsChangedSince(ctx, accountID, since, limit)
	if err != nil {
		return nil, err
	}
	return submissionRows(intents), nil
}

// Enqueue implements SubmissionStore: the row lands queued with not_before =
// now + the undo window (W-A3 — the window IS the row, no timer exists).
func (a *SubmissionAdapter) Enqueue(ctx context.Context, accountID int64, spec SubmissionSpec) (SubmissionRow, error) {
	payload, err := json.Marshal(submit.IntentEnvelope{
		IdentityID: spec.IdentityID,
		MailFrom:   spec.MailFrom,
		RcptTo:     spec.RcptTo,
	})
	if err != nil {
		return SubmissionRow{}, fmt.Errorf("encoding the submission payload: %w", err)
	}
	notBefore := time.Now().Add(spec.UndoWindow)
	in, err := a.store.EnqueueSendIntent(ctx, accountID, spec.EmailID, spec.MessageRFCID, payload, notBefore)
	if err != nil {
		return SubmissionRow{}, err
	}
	a.notify(accountID)
	return submissionRow(in), nil
}

// Cancel implements SubmissionStore over the store's compare-and-set.
func (a *SubmissionAdapter) Cancel(ctx context.Context, accountID, id int64) (SubmissionRow, error) {
	in, err := a.store.CancelSendIntent(ctx, accountID, id)
	switch {
	case err == nil:
		a.notify(accountID)
		return submissionRow(in), nil
	case errors.Is(err, store.ErrSubmissionNotCancelable):
		return SubmissionRow{}, ErrCannotUnsend
	case errors.Is(err, store.ErrNotFound):
		return SubmissionRow{}, ErrNotFound
	default:
		return SubmissionRow{}, err
	}
}

// Destroy implements SubmissionStore.
func (a *SubmissionAdapter) Destroy(ctx context.Context, accountID, id int64) (SubmissionRow, error) {
	in, err := a.store.DestroySendIntent(ctx, accountID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return SubmissionRow{}, ErrNotFound
		}
		return SubmissionRow{}, err
	}
	a.notify(accountID)
	return submissionRow(in), nil
}

func (a *SubmissionAdapter) notify(accountID int64) {
	if a.notifier != nil {
		a.notifier.Notify(accountID)
	}
}

// ---------------------------------------------------------------------------
// row mapping
// ---------------------------------------------------------------------------

func submissionRows(intents []store.SendIntent) []SubmissionRow {
	out := make([]SubmissionRow, 0, len(intents))
	for _, in := range intents {
		out = append(out, submissionRow(in))
	}
	return out
}

// submissionRow derives the JMAP view of one intent row.
func submissionRow(in store.SendIntent) SubmissionRow {
	row := SubmissionRow{
		ID:         in.ID,
		EmailID:    in.EmailID,
		SendAt:     in.NotBefore,
		UndoStatus: undoStatusOf(in),
		SMTPReply:  in.AcceptedReply,
		Destroyed:  in.DestroyedAt != nil,
		CreatedAt:  in.CreatedAt,
		UpdatedAt:  in.UpdatedAt,
	}
	var env submit.IntentEnvelope
	if err := json.Unmarshal(in.Payload, &env); err == nil {
		row.IdentityID = env.IdentityID
		row.MailFrom = env.MailFrom
		row.RcptTo = env.RcptTo
	}
	if in.State == store.IntentFailed && !in.Accepted() {
		row.Failed = true
		row.SMTPReply = in.LastError
	}
	return row
}

// undoStatusOf derives RFC 8621 §7.1's undoStatus from the persisted state
// machine (internal/submit doc.go):
//
//   - canceled: the CAS won — the message never left.
//   - pending: still queued with no acceptance. This includes a row whose
//     window has passed but that no executor has claimed yet, which is the
//     truth: the cancel CAS would still succeed against it.
//   - final: everything else — claimed, accepted, done, or permanently
//     failed. §7.1's final ("The message has been sent") is stretched by the
//     failed case, but §7.1 offers only three values and a failed submission
//     is certainly not cancelable; deliveryStatus (delivered:"no") is where
//     the failure is told precisely.
func undoStatusOf(in store.SendIntent) string {
	switch {
	case in.State == store.IntentCanceled:
		return "canceled"
	case !in.Accepted() && in.State == store.IntentQueued:
		return "pending"
	default:
		return "final"
	}
}

// submissionStateString is the EmailSubmission state: the send intents'
// watermark and count in the same "<nanos>-<count>" grammar as every other
// type's state, so /changes shares cursorFromState with them.
func submissionStateString(ctx context.Context, st *store.Store, accountID int64) (string, error) {
	watermark, err := st.SendIntentWatermark(ctx, accountID)
	if err != nil {
		return "", err
	}
	count, err := st.CountSendIntents(ctx, accountID)
	if err != nil {
		return "", err
	}
	return stateFor(watermark, count), nil
}

// ---------------------------------------------------------------------------
// uploads (RFC 8620 §6.1)
// ---------------------------------------------------------------------------

// UploadBlob implements jmaphttp's BlobUploader: the bytes go into the
// content-addressed store and the ACCOUNT takes a pin reference on them.
//
// The pin is what makes an upload account-scoped: OpenBlob's ownership check
// reads blob_refs, so the uploader can download and attach its blob and
// nobody else can name it (the same no-oracle property downloads have). The
// pin's owner id is the ACCOUNT id — schema 0002 models a pin as "a
// deliberate hold with no owner", and keying it per account is what lets two
// accounts upload identical bytes and each hold their own reference, while a
// re-upload by the same account dedupes onto one row.
//
// Retention: the pin holds the blob until blob.ExpirePins releases it (a
// daily sweep wired in cmd/moovd), after which the ordinary GC grace period
// still applies — comfortably beyond §6.1's "SHOULD keep for at least one
// hour". An upload that got attached meanwhile lives on through the message's
// own reference, so the pin's expiry never orphans real mail.
func (a *Adapter) UploadBlob(ctx context.Context, accountID int64, r io.Reader) (string, int64, error) {
	hash, size, err := a.blobs.Put(ctx, r)
	if err != nil {
		return "", 0, err
	}
	if err := a.blobs.AddRefTx(ctx, hash, accountID, blob.OwnerPin, accountID); err != nil {
		return "", 0, fmt.Errorf("pinning the uploaded blob: %w", err)
	}
	return hash.String(), size, nil
}

// EmailSubmissionState exposes the submission state on the SAME reader that
// answers every other type's state (deps.State, the object jmaphttp's
// EventSource consults) — which is what makes a pushed EmailSubmission state
// string equal the one EmailSubmission/get returns, the §7.1 identity the
// whole push design rests on.
func (a *Adapter) EmailSubmissionState(ctx context.Context, accountID int64) (string, error) {
	return submissionStateString(ctx, a.store, accountID)
}
