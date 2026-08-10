package imap

import (
	"context"
	"fmt"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// goStoreClient is the slice of *imapclient.Client that the STORE path uses.
//
// It is unexported and lives here rather than in client.go because it is an
// implementation seam, not part of the contract in L2 §4.1.
type goStoreClient interface {
	Store(numSet goimap.NumSet, store *goimap.StoreFlags, options *goimap.StoreOptions) *imapclient.FetchCommand
	Fetch(numSet goimap.NumSet, options *goimap.FetchOptions) *imapclient.FetchCommand
}

// The real client satisfies the seam.
var _ goStoreClient = (*imapclient.Client)(nil)

// messageState is the minimum a conflict decision needs to know about a
// message: which one it is and when it last changed.
type messageState struct {
	uid    UID
	modSeq ModSeq
}

// flagReader reads back the current state of a UID set.
//
// This narrower seam exists so the conflict-detection logic — the part that
// decides whether a conditional write silently failed (S2 H6) — is testable
// without a server. *imapclient.FetchCommand cannot be constructed from
// outside its package, so an interface returning one is not stubbable; a
// function returning plain data is. And since that decision is the one place
// in this package where a bug corrupts flag state rather than merely failing,
// it is the one that most needs to be tested away from the network.
type flagReader func(ctx context.Context, uids []UID) ([]messageState, error)

// readFlagsVia builds a flagReader backed by a real connection.
func readFlagsVia(gc goStoreClient) flagReader {
	return func(ctx context.Context, uids []UID) ([]messageState, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cmd := gc.Fetch(uidSetFromUIDs(uids), &goimap.FetchOptions{
			UID: true, ModSeq: true, Flags: true,
		})
		msgs, err := cmd.Collect()
		if err != nil {
			return nil, fmt.Errorf("imap: read-back FETCH after conditional STORE: %w", err)
		}
		out := make([]messageState, 0, len(msgs))
		for _, m := range msgs {
			if m.UID == 0 {
				continue
			}
			out = append(out, messageState{uid: UID(m.UID), modSeq: ModSeq(m.ModSeq)})
		}
		return out, nil
	}
}
