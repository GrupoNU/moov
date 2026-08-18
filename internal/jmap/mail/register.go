package mail

import (
	"context"
	"fmt"

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

	// Search answers Email/query (J3). It takes translated filters, so the
	// decisions about what this server can answer stay in query.go.
	Search SearchReader

	// Changes feeds Email/changes and Mailbox/changes (J3).
	Changes ChangesReader

	// Writer applies Email/set mutations (W1). It stays nil on a read-only
	// deployment, in which case RegisterSetMethods must not be called — the
	// session then keeps advertising the truth the old way.
	Writer EmailWriter

	// Mailboxer applies Mailbox/set mutations (W2). Same rule as Writer: nil
	// means the folder-mutation surface is not mounted.
	Mailboxer MailboxWriter

	// MailboxDelimiter is the IMAP hierarchy separator this account's server
	// uses, which Mailbox/set composes full paths with. Empty means "/", which
	// is what our Dovecot reports (S1/S2) and what nearly every server uses.
	//
	// It is configuration rather than a value read per call because the reader
	// contract does not carry it: MailboxRow has no delimiter, deliberately —
	// JMAP expresses hierarchy with parentId, so the READ path never needs one
	// (contracts.go).
	MailboxDelimiter string

	// SearchWindow overrides how deep Email/query looks. The zero value means
	// DefaultSearchWindow, which is the store's own cap; tests set it smaller
	// to exercise the truncation paths without seeding 200 messages.
	//
	// It may never EXCEED the store's cap: the store would silently clamp, and
	// Email/query's "a short result set means the window was not filled, so
	// this total is exact" reasoning would then be wrong. RegisterQueryMethods
	// enforces that.
	SearchWindow int

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

// RegisterQueryMethods registers the query/changes mail methods (J3).
//
// It is separate from RegisterGetMethods because the two epics have disjoint
// scopes and either can be mounted without the other — which is also what lets
// J2's tests keep registering only the get family. cmd/moovd calls both.
//
// The /queryChanges methods are registered even though they only ever decline:
// a client that sees unknownMethod concludes the server is partial, whereas
// cannotCalculateChanges is a conforming answer it knows how to handle
// (changes.go documents the decision against RFC 8620 §5.6).
func RegisterQueryMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterQueryMethods requires a registry and deps")
	}
	if deps.Search == nil || deps.Changes == nil || deps.State == nil {
		panic("mail: RegisterQueryMethods requires Search, Changes and State readers")
	}
	// A window deeper than the store's own cap cannot be honored: the store
	// clamps, and Email/query would then believe an exhausted result set was a
	// truncated one. Failing at startup beats serving a wrong total.
	if deps.SearchWindow > DefaultSearchWindow {
		panic(fmt.Sprintf("mail: SearchWindow %d exceeds the store's cap of %d",
			deps.SearchWindow, DefaultSearchWindow))
	}

	registry.Register("Email/query", jmap.CapMail, deps.handleEmailQuery)
	registry.Register("Email/changes", jmap.CapMail, deps.handleEmailChanges)
	registry.Register("Mailbox/changes", jmap.CapMail, deps.handleMailboxChanges)
	registry.Register("Email/queryChanges", jmap.CapMail, deps.handleEmailQueryChanges)
	registry.Register("Mailbox/queryChanges", jmap.CapMail, deps.handleMailboxQueryChanges)
}

// RegisterSetMethods registers the write-family mail methods (W1: Email/set;
// W2: Mailbox/set).
//
// Separate from the get and query families for the same reason those are
// separate from each other: disjoint epics, independently mountable, and a
// read-only deployment simply never calls this.
//
// It panics on a missing dependency, matching the other registrars: a /set
// surface with nothing to write through is a wiring bug that must fail at
// startup, never at the first click that tries to archive a message.
//
// Mailbox/set additionally needs Mailboxes (to read the tree it composes paths
// from) and Search (to enumerate a folder's messages for
// onDestroyRemoveEmails). Both are already required by the get and query
// families, so a deployment that mounts writes has them.
func RegisterSetMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterSetMethods requires a registry and deps")
	}
	if deps.Writer == nil || deps.Emails == nil || deps.State == nil {
		panic("mail: RegisterSetMethods requires Writer, Emails and State")
	}
	if deps.Mailboxer == nil || deps.Mailboxes == nil || deps.Search == nil {
		panic("mail: RegisterSetMethods requires Mailboxer, Mailboxes and Search for Mailbox/set")
	}

	registry.Register("Email/set", jmap.CapMail, deps.handleEmailSet)
	registry.Register("Mailbox/set", jmap.CapMail, deps.handleMailboxSet)
}
