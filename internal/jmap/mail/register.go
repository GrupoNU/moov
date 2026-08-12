package mail

import (
	"context"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/parser"
)

// Deps is everything the mail methods read through.
//
// Every field is an interface, which is what makes the handlers testable with
// fakes and keeps this package's dependency on the store confined to
// adapter.go. Construct it with NewDeps for the real store-backed wiring, or
// literally in tests.
type Deps struct {
	Mailboxes MailboxReader
	Emails    EmailReader
	Threads   ThreadReader
	Blobs     BlobReader
	State     StateReader

	// Limits are the server's declared limits. The same struct the session
	// advertises must be passed here, because CheckObjectsInGet is the
	// enforcement point for maxObjectsInGet and declared == applied is an AC
	// (jmaphttp/server_test.go).
	Limits jmap.Limits

	// ParserLimits bound the on-demand re-parse that produces bodyValues. The
	// zero value means parser.DefaultLimits.
	ParserLimits parser.Limits
}

// StateReader supplies the "state" string each /get response carries.
//
// RFC 8620 §5.1 defines it as "A (preferably short) string representing the
// state on the server for all the data of this type in the account", and §5.2
// makes it the cursor a client passes to /changes. J3 owns /changes; this
// interface exists now so that the state strings J2 emits are already the ones
// J3 will have to honor, rather than a stub J3 must replace everywhere.
type StateReader interface {
	MailboxState(ctx context.Context, accountID int64) (string, error)
	EmailState(ctx context.Context, accountID int64) (string, error)
	ThreadState(ctx context.Context, accountID int64) (string, error)
}

// RegisterGetMethods registers the get-family mail methods (J2) on a registry.
//
// It is the single entry point cmd/moovd calls, so adding a method is a change
// here rather than in the daemon. J3's query/changes family gets its own
// RegisterQueryMethods alongside this one, sharing the same Deps.
//
// It panics on a nil dependency, matching Registry.Register's own contract:
// a missing reader is a wiring bug that must fail at startup, never at the
// first request that touches it.
func RegisterGetMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterGetMethods requires a registry and deps")
	}
	if deps.Mailboxes == nil || deps.Emails == nil || deps.Threads == nil || deps.State == nil {
		panic("mail: RegisterGetMethods requires Mailboxes, Emails, Threads and State readers")
	}
	if deps.ParserLimits == (parser.Limits{}) {
		deps.ParserLimits = parser.DefaultLimits()
	}

	registry.Register("Mailbox/get", jmap.CapMail, deps.handleMailboxGet)
	registry.Register("Thread/get", jmap.CapMail, deps.handleThreadGet)
	registry.Register("Email/get", jmap.CapMail, deps.handleEmailGet)
}
