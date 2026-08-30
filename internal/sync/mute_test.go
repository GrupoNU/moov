package sync

import (
	"encoding/json"
	"testing"
)

// The mute escape hatches (L3 epic E4, canon §2.2).
//
// These test the DECISION, not the IMAP that follows it, and that split is
// deliberate: the decision is where a wrong answer costs the user a message
// they needed to see, and it is a pure function of the message's headers and
// the account's own addresses — so it can be tested exhaustively, in
// microseconds, against the canon's exact wording.
//
// The canon's three hatches, verbatim from support.google.com/mail/answer
// /16594169:
//
//	1. "The message is only sent to you."
//	2. "The message is sent to a Google Group you're in."
//	3. "Someone adds you to the 'To' or 'Cc' fields."
//
// Hatch 2 is NOT implemented and cannot be without a group directory; the test
// at the bottom pins that gap so it stays a documented decision rather than
// becoming a forgotten bug.

// addressDoc builds the stored address JSON the parser writes.
func addressDoc(t *testing.T, to, cc []string) []byte {
	t.Helper()
	type addr struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	doc := map[string][]addr{}
	for _, a := range to {
		doc["to"] = append(doc["to"], addr{Email: a})
	}
	for _, a := range cc {
		doc["cc"] = append(doc["cc"], addr{Email: a})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encoding the address doc: %v", err)
	}
	return raw
}

func TestMuteEscapeHatches(t *testing.T) {
	// The account's own addresses: the mailbox plus one alias identity, which
	// is how an alias reaches the same mailbox (migration 0006's Identity rows).
	own := map[string]bool{
		"diego@gruponu.com": true,
		"ventas@vastu.test": true,
	}

	cases := []struct {
		name        string
		to, cc      []string
		wantArchive bool
		wantReason  string
	}{{
		// HATCH 1, verbatim: "The message is only sent to you."
		name:        "sole recipient reaches the inbox",
		to:          []string{"diego@gruponu.com"},
		wantArchive: false,
		wantReason:  MuteHatchSoleRecipient,
	}, {
		// HATCH 3, verbatim: "Someone adds you to the 'To' or 'Cc' fields."
		name:        "named in To among others reaches the inbox",
		to:          []string{"equipo@otra.test", "diego@gruponu.com"},
		wantArchive: false,
		wantReason:  MuteHatchExplicitRecipient,
	}, {
		name:        "named in Cc reaches the inbox",
		to:          []string{"equipo@otra.test"},
		cc:          []string{"diego@gruponu.com"},
		wantArchive: false,
		wantReason:  MuteHatchExplicitRecipient,
	}, {
		// An ALIAS counts as the account. Without this, a message addressed to
		// a send-as address would be archived even though the user was named —
		// which is hatch 3 failing on exactly the address the user publishes.
		name:        "an identity alias counts as being named",
		to:          []string{"equipo@otra.test"},
		cc:          []string{"ventas@vastu.test"},
		wantArchive: false,
		wantReason:  MuteHatchExplicitRecipient,
	}, {
		// The local part of an address is technically case-sensitive (RFC 5321
		// §2.4) and no real mail system honors that. Treating it as sensitive
		// would make the feature look broken for the same user's own address.
		name:        "the comparison folds case",
		to:          []string{"Diego@GrupoNU.com"},
		wantArchive: false,
		wantReason:  MuteHatchSoleRecipient,
	}, {
		// THE DEFAULT, and the whole point of mute: a reply to the thread that
		// does not name the user goes to the archive.
		name:        "a reply that does not name you is archived",
		to:          []string{"equipo@otra.test", "alguien@otra.test"},
		wantArchive: true,
	}, {
		// A list post. This is the case hatch 2 would have caught in Gmail and
		// that we archive — see TestMuteGroupHatchIsUnimplemented.
		name:        "a mailing-list post is archived",
		to:          []string{"lista@grupos.test"},
		wantArchive: true,
	}, {
		// No recipients at all we can read. Archiving is the conservative
		// direction for a MUTED thread: the user asked for silence, and an
		// unreadable recipient list is not evidence they were named.
		name:        "no readable recipients is archived",
		wantArchive: true,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evaluateMuteHatches(addressDoc(t, c.to, c.cc), own)
			if got.Archive != c.wantArchive {
				t.Fatalf("Archive = %v, want %v (reason %q)", got.Archive, c.wantArchive, got.Reason)
			}
			if !c.wantArchive && got.Reason != c.wantReason {
				t.Errorf("hatch = %q, want %q", got.Reason, c.wantReason)
			}
			if c.wantArchive && got.Reason != "" {
				t.Errorf("an archived message named a hatch: %q", got.Reason)
			}
		})
	}
}

// TestMuteBccIsNotConsulted holds a deliberate omission that would otherwise
// look like an oversight.
//
// RFC 5322 §3.6.3 has the Bcc field removed on delivery, so a message that
// LOOKS sole-recipient may have been blind-copied to a crowd — we cannot know.
// The evaluator therefore reads only To and Cc, which is also exactly what the
// canon's hatch 3 names.
func TestMuteBccIsNotConsulted(t *testing.T) {
	own := map[string]bool{"diego@gruponu.com": true}

	// A bcc naming the account does NOT open a hatch: hatch 3 is about To and
	// Cc, and a Bcc we can see is one our own server added, not one the sender
	// used to name the user publicly.
	raw := []byte(`{"to":[{"email":"equipo@otra.test"}],"bcc":[{"email":"diego@gruponu.com"}]}`)
	if got := evaluateMuteHatches(raw, own); !got.Archive {
		t.Errorf("a Bcc opened an escape hatch (reason %q); only To and Cc do", got.Reason)
	}
}

// TestMuteMalformedAddressesArchive pins the failure direction.
func TestMuteMalformedAddressesArchive(t *testing.T) {
	own := map[string]bool{"diego@gruponu.com": true}
	for _, raw := range [][]byte{nil, []byte(""), []byte("not json"), []byte(`{"to":"a string"}`)} {
		if got := evaluateMuteHatches(raw, own); !got.Archive {
			t.Errorf("unparseable addresses %q opened a hatch (%q)", raw, got.Reason)
		}
	}
}

// TestMuteGroupHatchIsUnimplemented is the test that keeps a documented gap
// documented.
//
// Gmail's second hatch — "The message is sent to a Google Group you're in" —
// depends on Google Workspace knowing the user's group memberships. Moov has
// no group directory: Dovecot knows one mailbox, and a mailing list the user
// subscribed to externally is invisible to it. mute.go's header records the
// three alternatives and why each was refused.
//
// This test asserts the CONSEQUENCE rather than the absence, so it fails the
// day someone implements the hatch — at which point they will find this test,
// read the name, and update it deliberately instead of discovering the gap
// from a user report.
func TestMuteGroupHatchIsUnimplemented(t *testing.T) {
	own := map[string]bool{"diego@gruponu.com": true}

	// A post to a list the user belongs to. Gmail would let this through;
	// Moov archives it.
	raw := addressDoc(t, []string{"moov-dev@lists.test"}, nil)
	got := evaluateMuteHatches(raw, own)
	if !got.Archive {
		t.Fatalf("group mail was let through with reason %q — if hatch 2 is now implemented, "+
			"update this test and mute.go's header rather than deleting either", got.Reason)
	}
	// The constant exists so the gap has a name in the code (plan P4: nothing
	// leaves the plan by omission).
	if MuteHatchGroup == "" {
		t.Error("the unimplemented hatch has no name in the code")
	}
}
