package mail

import (
	"context"
	"errors"
	"io"
	"time"
)

// The reader contracts of L2-jmap-server §4.
//
// The spec puts these interfaces in internal/jmap. They live here instead, and
// the difference is deliberate: the interfaces are stated in terms of THIS
// package's view types (MailboxRow, EmailRow, …), and a type mentioned in an
// interface is imported by whoever declares it. Declaring them in
// internal/jmap would therefore force the row types up into the protocol
// package, which is exactly the storage-shaped detail §4's purity rule exists
// to keep out of it. The property §4 actually wants — handlers depend on
// interfaces, not on *store.Store — holds either way, and it is what the fakes
// in the tests exercise.
//
// Every method takes the account id explicitly rather than reading it from
// ctx. Authorization is then a value the compiler makes the caller supply at
// each call, instead of an ambient fact a handler can forget to scope by.

// ErrNotFound is what every reader returns for an object that does not exist,
// or that exists but belongs to another account. The two are deliberately
// indistinguishable: a JMAP /get answers with a notFound id either way, and a
// distinguishable "exists but forbidden" would turn every /get into an
// existence oracle for other people's mail.
var ErrNotFound = errors.New("mail: not found")

// MailboxReader reads the account's folder tree.
type MailboxReader interface {
	// Mailboxes returns every mailbox of the account, with counts resolved.
	// The order is the reader's; handlers do not rely on it.
	Mailboxes(ctx context.Context, accountID int64) ([]MailboxRow, error)

	// MailboxesByID returns the requested mailboxes. Ids that do not exist or
	// belong to another account are simply absent from the result — the
	// handler turns absence into the notFound array, which is the same answer
	// in both cases by design.
	MailboxesByID(ctx context.Context, accountID int64, ids []int64) ([]MailboxRow, error)
}

// EmailReader reads messages: metadata always, raw bytes only when a body is
// actually requested.
type EmailReader interface {
	// EmailsByID returns the requested messages' metadata. As with mailboxes,
	// unknown or foreign ids are absent rather than an error.
	EmailsByID(ctx context.Context, accountID int64, ids []int64) ([]EmailRow, error)

	// RawMessage opens the raw RFC 5322 bytes of a message. The caller closes
	// the reader. It is separate from EmailsByID because the overwhelming
	// majority of Email/get calls want metadata only, and opening a blob per
	// message to answer them would be pure waste.
	RawMessage(ctx context.Context, accountID int64, messageID int64) (io.ReadCloser, error)
}

// ThreadReader resolves an account's threads.
//
// See thread.go: the store has no thread column yet, so the adapter derives
// threads from References/In-Reply-To. This interface is the seam that lets a
// store-backed implementation replace that derivation without any handler
// changing.
type ThreadReader interface {
	// ThreadsByID returns the requested threads. Unknown ids are absent.
	ThreadsByID(ctx context.Context, accountID int64, ids []string) ([]ThreadRow, error)
}

// BlobReader serves blob downloads.
type BlobReader interface {
	// OpenBlob opens a blob the account holds a reference to, returning its
	// size alongside the content. It returns ErrNotFound both when the blob
	// does not exist and when this account does not reference it — the
	// no-oracle rule of the download route (server.go handleDownload).
	OpenBlob(ctx context.Context, accountID int64, blobID string) (io.ReadCloser, int64, error)
}

// MailboxRow is one folder as the JMAP layer needs it: the store's mailbox
// plus the four counts RFC 8621 §2 requires.
type MailboxRow struct {
	ID       int64
	Name     string
	ParentID int64 // 0 when the mailbox is at the top level

	// Role is the normalized SPECIAL-USE role ("inbox", "sent", …) or "" for
	// an ordinary folder. The store's vocabulary; role.go maps it to JMAP's.
	Role string

	SortOrder    uint64
	IsSubscribed bool

	TotalEmails   uint64
	UnreadEmails  uint64
	TotalThreads  uint64
	UnreadThreads uint64
}

// EmailRow is one message's stored metadata: everything an Email/get can
// answer without touching the raw bytes.
type EmailRow struct {
	ID       int64
	ThreadID string

	// BlobID is the sha256 hex of the raw message.
	BlobID string
	Size   uint64

	// MailboxIDs are the store ids of the mailboxes holding this message.
	// Phase 1 stores exactly one (message_state is 1:1 with a message), but
	// the JMAP shape is a set and A6's label work will populate more, so the
	// contract is plural from the start.
	MailboxIDs []int64

	// Keywords are JMAP keywords, already mapped from IMAP flags and stored
	// keywords by the adapter (keywords.go).
	Keywords []string

	ReceivedAt time.Time
	SentAt     time.Time
	HasSentAt  bool

	Subject   string
	MessageID []string
	InReplyTo []string
	Reference []string

	// Addresses is the parsed content of the store's addresses column, keyed
	// by JMAP property name ("from", "to", "cc", "bcc", "replyTo", "sender").
	Addresses map[string][]EmailAddress

	HasAttachment bool
	Preview       string

	// Structure is the stored MIME part tree, flattened, without content.
	Structure []StructurePart

	// ParseFailed reports that the MIME cascade could not extract structure
	// (parse_status = 'failed'). Such a message is served as a minimal Email
	// whose raw blob is still downloadable — honesty over a fabricated body
	// (L2 §2.4, S4).
	ParseFailed bool
}

// EmailAddress is one address in the RFC 8621 §4.1.2 EmailAddress shape.
type EmailAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// StructurePart is one node of the stored MIME tree — the shape
// internal/sync's encodeStructure writes into messages.mime_structure.
type StructurePart struct {
	Index       int    `json:"index"`
	Parent      int    `json:"parent"`
	Depth       int    `json:"depth"`
	MediaType   string `json:"mediaType"`
	Charset     string `json:"charset,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentID   string `json:"contentId,omitempty"`
	Size        int    `json:"size"`

	IsAttachment bool `json:"isAttachment,omitempty"`
	IsMultipart  bool `json:"isMultipart,omitempty"`
	IsRFC822     bool `json:"isRfc822,omitempty"`
	Partial      bool `json:"partiallyDecoded,omitempty"`
}

// ThreadRow is one thread: its id and its messages, oldest first.
type ThreadRow struct {
	ID string
	// EmailIDs are store message ids ordered by receivedAt, oldest first —
	// the order RFC 8621 §3 mandates for the emailIds property.
	EmailIDs []int64
}
