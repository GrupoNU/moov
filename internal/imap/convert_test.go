package imap

import (
	"reflect"
	"testing"

	goimap "github.com/emersion/go-imap/v2"
)

func TestRoleFromAttrs(t *testing.T) {
	tests := []struct {
		name  string
		mbox  string
		attrs []goimap.MailboxAttr
		want  MailboxRole
	}{
		{"sent", "Sent", []goimap.MailboxAttr{goimap.MailboxAttrSent}, RoleSent},
		{"trash", "Papelera", []goimap.MailboxAttr{goimap.MailboxAttrTrash}, RoleTrash},
		{"drafts", "Borradores", []goimap.MailboxAttr{goimap.MailboxAttrDrafts}, RoleDrafts},
		{"junk", "Spam", []goimap.MailboxAttr{goimap.MailboxAttrJunk}, RoleJunk},
		{"archive", "Archive", []goimap.MailboxAttr{goimap.MailboxAttrArchive}, RoleArchive},
		{
			// INBOX carries no SPECIAL-USE attribute anywhere: RFC 3501 makes
			// the name itself reserved. Detecting it by name is not a
			// heuristic, it is the specification.
			"inbox by name", "INBOX", nil, RoleInbox,
		},
		{"inbox is case-insensitive", "inbox", nil, RoleInbox},
		{"ordinary folder", "Proyectos/2026", nil, RoleNone},
		{
			// \Important (RFC 8457) is a per-message hint exposed as a virtual
			// folder. Giving it a role would make the engine sync a view as if
			// it were storage.
			"important is not a role", "Important",
			[]goimap.MailboxAttr{goimap.MailboxAttrImportant}, RoleNone,
		},
		{
			"attributes win over the name",
			"INBOX.Sent", []goimap.MailboxAttr{goimap.MailboxAttrSent}, RoleSent,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := roleFromAttrs(tc.mbox, tc.attrs); got != tc.want {
				t.Errorf("roleFromAttrs(%q, %v) = %q, want %q", tc.mbox, tc.attrs, got, tc.want)
			}
		})
	}
}

func TestSplitFlags(t *testing.T) {
	tests := []struct {
		name         string
		in           []goimap.Flag
		wantSystem   []string
		wantKeywords []string
		why          string
	}{
		{
			name:       "system flags are normalized",
			in:         []goimap.Flag{goimap.FlagSeen, goimap.FlagFlagged},
			wantSystem: []string{"seen", "flagged"},
			why:        "the store and JMAP both want $seen, not \\Seen",
		},
		{
			name:         "user keywords pass through verbatim",
			in:           []goimap.Flag{"$MoovL7", "NonJunk"},
			wantKeywords: []string{"$MoovL7", "NonJunk"},
			why:          "label keywords (A6) must survive the round trip byte for byte",
		},
		{
			name:       "case is folded for system flags",
			in:         []goimap.Flag{"\\SEEN", "\\deleted"},
			wantSystem: []string{"seen", "deleted"},
			why:        "RFC 3501 makes flags case-insensitive",
		},
		{
			name: "recent is dropped",
			in:   []goimap.Flag{goimap.FlagSeen, "\\Recent"},
			// \Recent is session-scoped and non-persistent by RFC 3501. Storing
			// it would record a fact that is already false when it is read.
			wantSystem: []string{"seen"},
			why:        "\\Recent is meaningless outside the session that saw it",
		},
		{
			name: "unknown backslash flags are kept as keywords",
			in:   []goimap.Flag{"\\Forwarded"},
			// Real state another client set. Dropping it would make Moov's
			// copy lossy, which breaks "Moov is a faithful cache".
			wantKeywords: []string{"\\Forwarded"},
			why:          "an unrecognized flag is still somebody's data",
		},
		{
			name:         "mixed",
			in:           []goimap.Flag{goimap.FlagAnswered, "$MoovL1", "\\Recent", goimap.FlagDraft},
			wantSystem:   []string{"answered", "draft"},
			wantKeywords: []string{"$MoovL1"},
			why:          "the common case",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			system, keywords := splitFlags(tc.in)
			if !equalStrings(system, tc.wantSystem) {
				t.Errorf("system = %v, want %v (%s)", system, tc.wantSystem, tc.why)
			}
			if !equalStrings(keywords, tc.wantKeywords) {
				t.Errorf("keywords = %v, want %v (%s)", keywords, tc.wantKeywords, tc.why)
			}
		})
	}
}

// TestFlagRoundTrip is the property that matters for labels: a keyword written
// to the server and read back must be the same keyword. If normalization were
// lossy, a label would silently rename itself on every sync.
func TestFlagRoundTrip(t *testing.T) {
	cases := []string{"seen", "answered", "flagged", "deleted", "draft", "$MoovL1", "$MoovL42", "NonJunk"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			wire := flagToGoIMAP(name)
			system, keywords := splitFlags([]goimap.Flag{wire})
			got := append(system, keywords...) //nolint:gocritic // one of the two is empty
			if len(got) != 1 || got[0] != name {
				t.Errorf("round trip of %q produced %v", name, got)
			}
		})
	}
}

func TestUIDsFromUIDSet(t *testing.T) {
	t.Run("expands a range", func(t *testing.T) {
		var set goimap.UIDSet
		set.AddRange(10, 13)
		got, truncated := uidsFromUIDSet(set, 100)
		if truncated {
			t.Error("unexpected truncation")
		}
		if want := []UID{10, 11, 12, 13}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("reports truncation instead of allocating", func(t *testing.T) {
		var set goimap.UIDSet
		set.AddRange(1, 1000)
		got, truncated := uidsFromUIDSet(set, 10)
		if !truncated {
			t.Error("truncation not reported")
		}
		if len(got) != 10 {
			t.Errorf("got %d UIDs, want the cap of 10", len(got))
		}
	})

	t.Run("a dynamic set is truncation, not an empty list", func(t *testing.T) {
		// "n:*" cannot be expanded at all. Returning an empty slice with no
		// signal would read as "nothing vanished" — the silent-empty-delta bug
		// that is hard to find. It must say truncated.
		var set goimap.UIDSet
		set.AddRange(5, 0)
		got, truncated := uidsFromUIDSet(set, 100)
		if !truncated {
			t.Error("a dynamic set must be reported as truncated")
		}
		if len(got) != 0 {
			t.Errorf("got %v, want nothing expandable", got)
		}
	})
}

func TestDedupeUIDs(t *testing.T) {
	// VANISHED reaches the client twice for a QRESYNC SELECT: once in
	// SelectData and once through the unilateral handler. Reporting an expunge
	// twice would make the sync engine's counters wrong.
	got := dedupeUIDs([]UID{7, 3, 7, 1, 3})
	if want := []UID{1, 3, 7}; !reflect.DeepEqual(got, want) {
		t.Errorf("dedupeUIDs = %v, want %v", got, want)
	}
	if got := dedupeUIDs(nil); got != nil {
		t.Errorf("dedupeUIDs(nil) = %v, want nil", got)
	}
}

func TestCapabilities(t *testing.T) {
	caps := capsFromGoIMAP(goimap.CapSet{
		goimap.CapIMAP4rev1: {},
		"QRESYNC":           {},
		"NOTIFY":            {},
	})

	// RFC 3501 makes capability names case-insensitive, and Dovecot sends them
	// uppercase while go-imap's constants are mixed case.
	for _, name := range []string{"QRESYNC", "qresync", "QResync", "NOTIFY"} {
		if !caps.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
	if caps.Has("OBJECTID") {
		// Our Dovecot does not advertise it (S2 T2a); a false positive here
		// would make the engine trust server-side message IDs that do not
		// exist.
		t.Error("Has(OBJECTID) = true, want false")
	}
}

func TestStatusFoldsIntoMailboxInfo(t *testing.T) {
	msgs, unseen := uint32(120), uint32(4)
	info := MailboxInfo{Name: "INBOX"}
	applyStatus(&info, &goimap.StatusData{
		Mailbox:       "INBOX",
		NumMessages:   &msgs,
		NumUnseen:     &unseen,
		UIDNext:       goimap.UID(500),
		UIDValidity:   1786153920,
		HighestModSeq: 42,
	})

	if !info.HasStatus {
		t.Error("HasStatus = false after applying a STATUS")
	}
	if info.NumMessages != 120 || info.NumUnseen != 4 {
		t.Errorf("counts = (%d, %d), want (120, 4)", info.NumMessages, info.NumUnseen)
	}
	if info.HighestModSeq != 42 {
		// This is the cursor incremental sync resumes from; losing it means a
		// full refetch of the mailbox.
		t.Errorf("HighestModSeq = %d, want 42", info.HighestModSeq)
	}
	if info.UIDValidity != 1786153920 {
		t.Errorf("UIDValidity = %d, want 1786153920", info.UIDValidity)
	}
}

// TestStatusPointersAreNotAssumed guards the trap in go-imap's StatusData: the
// counters are pointers because zero is a legitimate value the server may or
// may not have sent. Dereferencing blindly would panic on a mailbox the server
// reported partially.
func TestStatusPointersAreNotAssumed(t *testing.T) {
	info := MailboxInfo{Name: "Empty"}
	applyStatus(&info, &goimap.StatusData{Mailbox: "Empty"})
	if info.NumMessages != 0 || info.NumUnseen != 0 {
		t.Errorf("absent counters produced (%d, %d), want zeroes", info.NumMessages, info.NumUnseen)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
