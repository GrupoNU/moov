package imap

import (
	"context"
	"fmt"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// statusItems is what Moov asks for in every STATUS. HighestModSeq is the one
// that matters most: it is the cursor the incremental sync resumes from.
func statusOptions() *goimap.StatusOptions {
	return &goimap.StatusOptions{
		NumMessages:   true,
		NumUnseen:     true,
		UIDNext:       true,
		UIDValidity:   true,
		HighestModSeq: true,
	}
}

// ListMailboxes implements Client.
//
// It asks for STATUS inside the LIST when the server advertises LIST-STATUS
// (ours does — S2 T2a), which turns one round trip per mailbox into one round
// trip total. Without it, the counters are fetched in a follow-up STATUS per
// selectable mailbox.
func (cl *client) ListMailboxes(ctx context.Context) ([]MailboxInfo, error) {
	gc, err := cl.conn()
	if err != nil {
		return nil, err
	}

	opts := &goimap.ListOptions{
		ReturnSubscribed: true,
		ReturnChildren:   true,
	}
	if cl.caps.Has(CapSpecialUse) {
		opts.ReturnSpecialUse = true
	}
	useListStatus := cl.caps.Has(CapListStatus)
	if useListStatus {
		opts.ReturnStatus = statusOptions()
	}

	cmd := gc.List("", "*", opts)
	data, err := cmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("imap: LIST: %w", err)
	}

	out := make([]MailboxInfo, 0, len(data))
	for _, d := range data {
		out = append(out, mailboxFromListData(d))
	}

	if !useListStatus {
		if err := cl.fillStatus(ctx, gc, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fillStatus is the fallback for a server without LIST-STATUS: one STATUS per
// selectable mailbox. It is sequential by construction — the connection is a
// single command stream — which is exactly why LIST-STATUS is worth using.
func (cl *client) fillStatus(ctx context.Context, gc *imapclient.Client, boxes []MailboxInfo) error {
	opts := statusOptions()
	for i := range boxes {
		if boxes[i].NoSelect {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := gc.Status(boxes[i].Name, opts).Wait()
		if err != nil {
			// A mailbox can disappear between the LIST and the STATUS. That
			// is a normal race, not a failure of the listing: skip it and let
			// the next pass see the new tree.
			cl.log.Debug("imap: STATUS failed, skipping mailbox",
				"mailbox", boxes[i].Name, "error", err)
			continue
		}
		applyStatus(&boxes[i], data)
	}
	return nil
}

// SelectQResync implements Client.
func (cl *client) SelectQResync(ctx context.Context, mailbox string, uidValidity uint32, modSeq ModSeq) (SelectResult, error) {
	var out SelectResult

	gc, err := cl.conn()
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}

	opts := &goimap.SelectOptions{}

	// QRESYNC is only meaningful when the caller has previous state to resync
	// against. Both values must be present: a UIDVALIDITY without a modseq
	// tells the server nothing about what the client already has.
	wantQResync := uidValidity != 0 && modSeq != 0
	if wantQResync {
		opts.QResync = &goimap.QResyncOptions{
			UIDValidity: uidValidity,
			ModSeq:      uint64(modSeq),
		}
	}

	// VANISHED (EARLIER) arrives during the SELECT. The patched library
	// collects it into SelectData.VanishedUIDs, but it also routes it through
	// the unilateral Vanished handler, so the collector below is armed for
	// both paths and de-duplicated afterwards.
	collected := cl.armVanishedCollector()
	defer cl.disarmVanishedCollector()

	data, err := gc.Select(mailbox, opts).Wait()
	if err != nil {
		return out, fmt.Errorf("imap: SELECT %q: %w", mailbox, err)
	}

	out.UIDValidity = data.UIDValidity
	out.HighestModSeq = ModSeq(data.HighestModSeq)
	out.UIDNext = UID(data.UIDNext)
	out.NumMessages = data.NumMessages
	// go-imap does not surface the [READ-ONLY] response code, so a mailbox is
	// read-only here only when Moov itself asked for EXAMINE — which it never
	// does today. The field is kept in SelectResult because the sync engine
	// must not write to a read-only mailbox, and the day EXAMINE is used the
	// answer has to come from here rather than from a caller's memory.
	out.ReadOnly = opts.ReadOnly

	// A UIDVALIDITY change is the server saying "your UIDs mean nothing any
	// more". It is reported, not raised: the sync engine's response is to drop
	// the mailbox's local state and resync, which is a normal branch of the
	// algorithm rather than a failure (L2 §2.5).
	if wantQResync && data.UIDValidity != uidValidity {
		out.UIDValidityChanged = true
		cl.log.Info("imap: UIDVALIDITY changed, local state for this mailbox is stale",
			"mailbox", mailbox, "was", uidValidity, "now", data.UIDValidity)
	}

	if !out.UIDValidityChanged {
		vanished := collected()
		if len(data.VanishedUIDs) > 0 {
			extra, truncated := uidsFromUIDSet(data.VanishedUIDs, maxVanishedUIDs)
			if truncated {
				cl.log.Warn("imap: VANISHED set too large to expand; treat as full resync",
					"mailbox", mailbox)
			}
			vanished = append(vanished, extra...)
		}
		out.VanishedUIDs = dedupeUIDs(vanished)
	}

	cl.mu.Lock()
	cl.selected = mailbox
	cl.mu.Unlock()

	return out, nil
}

// dedupeUIDs sorts and de-duplicates, because VANISHED can reach the client by
// two paths (SelectData and the unilateral handler) for the same expunge.
func dedupeUIDs(uids []UID) []UID {
	if len(uids) == 0 {
		return nil
	}
	seen := make(map[UID]struct{}, len(uids))
	out := make([]UID, 0, len(uids))
	for _, u := range uids {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	sortUIDs(out)
	return out
}
