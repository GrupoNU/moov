package sync

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/parser"
	"github.com/GrupoNU/moov/internal/store"
)

// Unit tests for the pure helpers. They need no database and run under
// `go test -short`, which is what keeps the fast feedback loop fast.

func TestStoreFlagsMapsSystemFlagsAndIgnoresKeywords(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want store.Flags
	}{
		{"empty", nil, 0},
		{"seen", []string{"seen"}, store.FlagSeen},
		{"backslash form", []string{`\Seen`}, store.FlagSeen},
		{"mixed case", []string{"SeEn", "FLAGGED"}, store.FlagSeen | store.FlagFlagged},
		{"all five", []string{"seen", "answered", "flagged", "deleted", "draft"},
			store.FlagSeen | store.FlagAnswered | store.FlagFlagged | store.FlagDeleted | store.FlagDraft},
		// A user keyword is not a system flag: it must not land in the bitmask,
		// because the bitmask is a closed set and a stray bit would be
		// indistinguishable from a real flag.
		{"keyword ignored", []string{"$MoovL3", "seen"}, store.FlagSeen},
		{"only keywords", []string{"$Forwarded", "$MDNSent"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := storeFlags(tc.in); got != tc.want {
				t.Errorf("storeFlags(%v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestStoreRoleCoversEveryIMAPRole(t *testing.T) {
	cases := map[imap.MailboxRole]store.MailboxRole{
		imap.RoleNone:    store.RoleNone,
		imap.RoleInbox:   store.RoleInbox,
		imap.RoleArchive: store.RoleArchive,
		imap.RoleDrafts:  store.RoleDrafts,
		imap.RoleSent:    store.RoleSent,
		imap.RoleJunk:    store.RoleJunk,
		imap.RoleTrash:   store.RoleTrash,
		imap.RoleAll:     store.RoleAll,
		imap.RoleFlagged: store.RoleFlagged,
	}
	for in, want := range cases {
		if got := storeRole(in); got != want {
			t.Errorf("storeRole(%q) = %q, want %q", in, got, want)
		}
	}

	// An unknown role degrades to "ordinary folder" rather than being stored
	// verbatim, which would violate the role CHECK constraint.
	if got := storeRole(imap.MailboxRole("invented")); got != store.RoleNone {
		t.Errorf("storeRole of an unknown role = %q, want %q", got, store.RoleNone)
	}
}

func TestUIDRange(t *testing.T) {
	cases := []struct {
		low, high imap.UID
		want      []imap.UID
	}{
		{1, 1, []imap.UID{1}},
		{3, 6, []imap.UID{3, 4, 5, 6}},
		{5, 4, nil}, // inverted: empty, not a panic
	}
	for _, tc := range cases {
		got := uidRange(tc.low, tc.high)
		if len(got) != len(tc.want) {
			t.Errorf("uidRange(%d,%d) has %d entries, want %d", tc.low, tc.high, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("uidRange(%d,%d)[%d] = %d, want %d", tc.low, tc.high, i, got[i], tc.want[i])
			}
		}
	}

	// The top of the UID space must terminate rather than wrap: the loop
	// compares against high instead of incrementing past it, and this is the
	// case that would hang if it did not.
	got := uidRange(^imap.UID(0)-2, ^imap.UID(0))
	if len(got) != 3 {
		t.Errorf("uidRange at the top of the UID space returned %d entries, want 3", len(got))
	}
}

func TestHighestUID(t *testing.T) {
	cases := []struct {
		name string
		sel  imap.SelectResult
		want imap.UID
	}{
		{"from uidnext", imap.SelectResult{UIDNext: 51, NumMessages: 20}, 50},
		{"empty mailbox", imap.SelectResult{UIDNext: 1, NumMessages: 0}, 0},
		{"no uidnext falls back to count", imap.SelectResult{NumMessages: 12}, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := highestUID(tc.sel); got != tc.want {
				t.Errorf("highestUID = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEqualFoldASCII(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"INBOX", "INBOX", true},
		{"INBOX", "inbox", true},
		{"InBoX", "iNbOx", true},
		{"INBOX", "INBOXES", false},
		{"INBOX", "Sent", false},
		{"", "", true},
	}
	for _, tc := range cases {
		if got := equalFoldASCII(tc.a, tc.b); got != tc.want {
			t.Errorf("equalFoldASCII(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestMessageDatePrefersAPlausibleHeader(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	internal := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)
	s := &Syncer{opts: Options{Clock: func() time.Time { return now }}}

	t.Run("valid header wins", func(t *testing.T) {
		header := "Tue, 05 Aug 2026 10:30:00 +0000"
		got := s.messageDate(header, internal)
		if got.UTC().Day() != 5 {
			t.Errorf("date = %s, want the header's 5 Aug", got)
		}
	})

	t.Run("unparseable header falls back to internaldate", func(t *testing.T) {
		if got := s.messageDate("not a date at all", internal); !got.Equal(internal) {
			t.Errorf("date = %s, want the internal date %s", got, internal)
		}
	})

	t.Run("absent header falls back to internaldate", func(t *testing.T) {
		if got := s.messageDate("", internal); !got.Equal(internal) {
			t.Errorf("date = %s, want the internal date %s", got, internal)
		}
	})

	// A message dated 1969 or 2140 is common in real mail and would sort to an
	// extreme of every list forever. INTERNALDATE is the honest answer.
	t.Run("implausible header is rejected", func(t *testing.T) {
		for _, header := range []string{
			"Thu, 01 Jan 1970 00:00:00 +0000",
			"Sat, 01 Jan 2140 00:00:00 +0000",
		} {
			if got := s.messageDate(header, internal); !got.Equal(internal) {
				t.Errorf("date for %q = %s, want the internal date", header, got)
			}
		}
	})

	// The column is NOT NULL, so there is always an answer.
	t.Run("nothing at all falls back to now", func(t *testing.T) {
		if got := s.messageDate("", time.Time{}); !got.Equal(now) {
			t.Errorf("date = %s, want now (%s)", got, now)
		}
	})
}

func TestSanitizeTextMakesStringsSafeForPostgres(t *testing.T) {
	t.Run("clean text is returned unchanged", func(t *testing.T) {
		const in = "Hola, ¿qué tal? — 日本語"
		if got := sanitizeText(in); got != in {
			t.Errorf("sanitizeText mangled clean text: %q", got)
		}
	})

	// PostgreSQL cannot store NUL in a text column at all: the insert fails,
	// taking the whole batch with it.
	t.Run("NUL is removed", func(t *testing.T) {
		got := sanitizeText("before\x00after")
		if strings.ContainsRune(got, 0) {
			t.Errorf("sanitizeText left a NUL in %q", got)
		}
		if got != "beforeafter" {
			t.Errorf("sanitizeText = %q, want %q", got, "beforeafter")
		}
	})

	t.Run("invalid UTF-8 is replaced", func(t *testing.T) {
		got := sanitizeText("ok\xff\xfebad")
		if !isValidUTF8(got) {
			t.Errorf("sanitizeText returned invalid UTF-8: %q", got)
		}
	})
}

func TestPreviewTruncatesOnARuneBoundary(t *testing.T) {
	t.Run("short text is unchanged", func(t *testing.T) {
		if got := preview("a short body"); got != "a short body" {
			t.Errorf("preview = %q", got)
		}
	})

	t.Run("whitespace is collapsed", func(t *testing.T) {
		if got := preview("line one\r\n\r\n   line   two"); got != "line one line two" {
			t.Errorf("preview = %q", got)
		}
	})

	t.Run("long text is cut without breaking a rune", func(t *testing.T) {
		// Multi-byte characters positioned so a byte-wise cut would split one.
		got := preview(strings.Repeat("é", 300))
		if len(got) > previewLength {
			t.Errorf("preview is %d bytes, want at most %d", len(got), previewLength)
		}
		if !isValidUTF8(got) {
			t.Errorf("preview split a rune: %q", got)
		}
	})
}

func TestEncodeAddressesIncludesBcc(t *testing.T) {
	h := parser.CanonHeaders{
		From: []parser.Address{{Name: "Sender", Address: "s@example.test"}},
		To:   []parser.Address{{Address: "to@example.test"}},
		Bcc:  []parser.Address{{Address: "blind@example.test"}},
	}
	got := string(encodeAddresses(h))

	// Bcc belongs in the structured column (the JMAP layer shows a draft's own
	// Bcc to its author) but must never reach the FTS text.
	for _, want := range []string{"from", "to", "bcc", "blind@example.test"} {
		if !strings.Contains(got, want) {
			t.Errorf("encoded addresses %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, `"cc"`) {
		t.Errorf("encoded addresses names an absent cc list: %q", got)
	}
}

func TestEncodeStructureOmitsPartContent(t *testing.T) {
	parts := []parser.Part{
		{Index: 0, Parent: -1, MediaType: "multipart/mixed", IsMultipart: true},
		{Index: 1, Parent: 0, MediaType: "text/plain", Content: []byte("secret body bytes"), Size: 17},
		{Index: 2, Parent: 0, MediaType: "application/pdf", Filename: "invoice.pdf", IsAttachment: true},
	}
	got := string(encodeStructure(parts))

	// The bytes live in the blob, which is the system of record. Copying them
	// into a jsonb column read on every message-list query would be the
	// expensive kind of redundant.
	if strings.Contains(got, "secret body bytes") {
		t.Errorf("encoded structure carries part content: %q", got)
	}
	for _, want := range []string{"multipart/mixed", "invoice.pdf", "application/pdf"} {
		if !strings.Contains(got, want) {
			t.Errorf("encoded structure %q is missing %q", got, want)
		}
	}
}

func TestStoreParseStatusMapping(t *testing.T) {
	cases := map[parser.ParseStatus]store.ParseStatus{
		parser.StatusOK:      store.ParseOK,
		parser.StatusPartial: store.ParsePartial,
		parser.StatusFailed:  store.ParseFailed,
	}
	for in, want := range cases {
		if got := storeParseStatus(in); got != want {
			t.Errorf("storeParseStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOptionsDefaults(t *testing.T) {
	got := Options{}.withDefaults()

	if got.RecentWindow != DefaultRecentWindow {
		t.Errorf("RecentWindow = %s, want %s", got.RecentWindow, DefaultRecentWindow)
	}
	if got.Connections != DefaultConnections {
		t.Errorf("Connections = %d, want %d", got.Connections, DefaultConnections)
	}
	if got.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize = %d, want %d", got.BatchSize, DefaultBatchSize)
	}
	if got.FetchWindow != DefaultFetchWindow {
		t.Errorf("FetchWindow = %d, want %d", got.FetchWindow, DefaultFetchWindow)
	}
	if got.ParseWorkers < 1 {
		t.Errorf("ParseWorkers = %d, want at least 1", got.ParseWorkers)
	}
	if got.Logger == nil || got.Clock == nil {
		t.Error("withDefaults left Logger or Clock nil")
	}

	// Explicit values survive.
	custom := Options{Connections: 7, BatchSize: 3}.withDefaults()
	if custom.Connections != 7 || custom.BatchSize != 3 {
		t.Errorf("withDefaults overwrote explicit values: %+v", custom)
	}
}

func TestNewRejectsAnEmptyConnectionPool(t *testing.T) {
	if _, err := New(nil, nil, nil, Options{}); err == nil {
		t.Error("New accepted a nil store")
	}
}

func TestResultRate(t *testing.T) {
	r := Result{RecentStored: 500, RecentElapsed: 2 * time.Second}
	if got := r.Rate(); got != 250 {
		t.Errorf("Rate = %v, want 250", got)
	}
	// A zero duration must not divide by zero.
	if got := (Result{RecentStored: 10}).Rate(); got != 0 {
		t.Errorf("Rate with no elapsed time = %v, want 0", got)
	}
}

func TestMailboxScopeIsStableAndDistinct(t *testing.T) {
	if mailboxScope(1) == mailboxScope(2) {
		t.Error("two mailboxes share a checkpoint scope")
	}

	// Stability is asserted against a literal, not against a second call to the
	// same function: comparing a function with itself would pass even if the
	// format string changed, which is precisely the regression that would
	// orphan every existing checkpoint in a deployed database.
	if got, want := mailboxScope(42), "mailbox:42"; got != want {
		t.Errorf("mailboxScope(42) = %q, want %q", got, want)
	}

	if mailboxScope(1) == store.AccountScope {
		t.Error("a mailbox scope collides with the reserved account scope")
	}
}

// The numeric conversions of convert.go. Each crosses a signedness or width
// boundary where a silent wrap would corrupt a message's identity, so each
// boundary is asserted rather than assumed.

func TestModSeqToDBClampsRatherThanWrapping(t *testing.T) {
	cases := []struct {
		name string
		in   imap.ModSeq
		want int64
	}{
		{"zero", 0, 0},
		{"ordinary", 4321, 4321},
		{"largest representable", math.MaxInt64, math.MaxInt64},
		// A wrap here would produce a NEGATIVE cursor, and a negative modseq
		// makes every later CHANGEDSINCE match everything — a silent, permanent
		// full resync on every pass.
		{"above int64", math.MaxInt64 + 1, math.MaxInt64},
		{"maximum uint64", math.MaxUint64, math.MaxInt64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := modSeqToDB(tc.in)
			if got != tc.want {
				t.Errorf("modSeqToDB(%d) = %d, want %d", tc.in, got, tc.want)
			}
			if got < 0 {
				t.Errorf("modSeqToDB(%d) produced a negative cursor: %d", tc.in, got)
			}
		})
	}
}

func TestUIDValidityRoundTrip(t *testing.T) {
	for _, v := range []uint32{0, 1, 4242, math.MaxUint32} {
		if got := uidValidityFromDB(uidValidityToDB(v)); got != v {
			t.Errorf("uidvalidity %d round-tripped to %d", v, got)
		}
	}
}

func TestUIDValidityFromDBRejectsImpossibleValues(t *testing.T) {
	// A value outside the uint32 range cannot have come from a server, so the
	// row is corrupt. Zero means "never synced", which makes the mailbox
	// resync from scratch instead of being compared against a fabricated
	// validity.
	for _, v := range []int64{-1, -99999, int64(math.MaxUint32) + 1, math.MaxInt64} {
		if got := uidValidityFromDB(v); got != 0 {
			t.Errorf("uidValidityFromDB(%d) = %d, want 0", v, got)
		}
	}
}

func TestWindowSizeClampsToTheUIDSpace(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want imap.UID
	}{
		{"zero falls back to the default", 0, imap.UID(DefaultFetchWindow)},
		{"negative falls back to the default", -5, imap.UID(DefaultFetchWindow)},
		{"ordinary", 500, 500},
	}
	// The saturating cases only exist where an int is wider than a uint32, so
	// they are appended rather than written as literals: on a 32-bit build the
	// constants below would not fit in an int and would not compile.
	if math.MaxInt > math.MaxUint32 {
		cases = append(cases,
			struct {
				name string
				in   int
				want imap.UID
			}{"maximum uid", int(uint32(math.MaxUint32)), imap.UID(math.MaxUint32)},
			struct {
				name string
				in   int
				want imap.UID
			}{"beyond the uid space", int(uint32(math.MaxUint32)) + 1, imap.UID(math.MaxUint32)},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowSize(tc.in); got != tc.want {
				t.Errorf("windowSize(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' && !strings.Contains(s, "�") {
			return false
		}
	}
	// The real check: range over a string yields RuneError for invalid bytes,
	// and a valid string round-trips through []rune unchanged.
	return string([]rune(s)) == s
}
