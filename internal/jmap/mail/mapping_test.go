package mail

import (
	"reflect"
	"testing"
)

// The store's flag bits, restated as the adapter passes them (store.FlagSeen
// is bit 0 and the schema pins it there).
const (
	flagSeen     uint64 = 1 << 0
	flagAnswered uint64 = 1 << 1
	flagFlagged  uint64 = 1 << 2
	flagDeleted  uint64 = 1 << 3
	flagDraft    uint64 = 1 << 4
	flagRecent   uint64 = 1 << 5
)

func TestKeywordsFromSystemFlags(t *testing.T) {
	got := jmapKeywords(flagSeen|flagAnswered|flagFlagged|flagDraft, nil)
	want := []string{KeywordAnswered, KeywordDraft, KeywordFlagged, KeywordSeen}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keywords = %v, want %v", got, want)
	}
}

// \Deleted and \Recent have no JMAP keyword and must not be invented.
func TestKeywordsDropFlagsWithNoJMAPEquivalent(t *testing.T) {
	got := jmapKeywords(flagDeleted|flagRecent, nil)
	if len(got) != 0 {
		t.Errorf("keywords = %v, want none: neither \\Deleted nor \\Recent has a JMAP keyword", got)
	}
}

func TestKeywordsCustomAndLabels(t *testing.T) {
	// A6 stores labels as IMAP keywords; their case must survive.
	got := jmapKeywords(0, []string{"$MoovL7", "Important", "$Seen"})
	want := []string{"$moovl7", "$seen", "Important"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keywords = %v, want %v", got, want)
	}
}

// A server that reports a system flag in the keywords column must not leak the
// IMAP spelling — "\Seen" is not a legal JMAP keyword.
func TestKeywordsNormalizeIMAPSpelling(t *testing.T) {
	got := jmapKeywords(0, []string{`\Seen`, `\Deleted`})
	want := []string{KeywordSeen}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keywords = %v, want %v", got, want)
	}
}

func TestKeywordsDeduplicate(t *testing.T) {
	// The flag and the literal keyword must collapse into one set member.
	got := jmapKeywords(flagSeen, []string{"$seen", "$seen"})
	if len(got) != 1 || got[0] != KeywordSeen {
		t.Errorf("keywords = %v, want exactly [$seen]", got)
	}
}

func TestKeywordsAreSortedAndStable(t *testing.T) {
	a := jmapKeywords(flagSeen|flagFlagged, []string{"zeta", "alpha"})
	b := jmapKeywords(flagFlagged|flagSeen, []string{"alpha", "zeta"})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("keyword order is not stable: %v vs %v", a, b)
	}
}

func TestKeywordSetRendersAsJMAPSet(t *testing.T) {
	set := keywordSet([]string{KeywordSeen, "$moovl7"})
	if set[KeywordSeen] != true || set["$moovl7"] != true || len(set) != 2 {
		t.Errorf("set = %#v", set)
	}
}

func TestIDsRoundTripAndAreCanonical(t *testing.T) {
	for _, id := range []int64{1, 35, 36, 1000, 1 << 40} {
		if got, err := DecodeMailboxID(EncodeMailboxID(id)); err != nil || got != id {
			t.Errorf("mailbox %d round trip = (%d, %v)", id, got, err)
		}
		if got, err := DecodeEmailID(EncodeEmailID(id)); err != nil || got != id {
			t.Errorf("email %d round trip = (%d, %v)", id, got, err)
		}
		if got, err := DecodeThreadID(EncodeThreadID(id)); err != nil || got != id {
			t.Errorf("thread %d round trip = (%d, %v)", id, got, err)
		}
	}
}

// RFC 8620 §1.2: an Id must not start with a digit, and clients must not be
// able to confuse it with a number.
func TestIDsNeverStartWithADigit(t *testing.T) {
	for _, id := range []int64{1, 9, 10, 99, 123456} {
		for _, s := range []string{EncodeMailboxID(id), EncodeEmailID(id), EncodeThreadID(id)} {
			if s == "" || (s[0] >= '0' && s[0] <= '9') {
				t.Errorf("id %q starts with a digit", s)
			}
		}
	}
}

// Two spellings must never name one object: a non-canonical id is rejected,
// not silently accepted as an alias.
func TestIDsRejectNonCanonicalSpellings(t *testing.T) {
	for _, bad := range []string{"m01", "M1", "m", "m-1", "m0", "1", "", "e1", "t1"} {
		if _, err := DecodeMailboxID(bad); err == nil {
			t.Errorf("DecodeMailboxID(%q) accepted a non-canonical or foreign id", bad)
		}
	}
	// The prefixes keep the three id spaces apart: a mailbox id is not an
	// email id even though the numbers coincide.
	if _, err := DecodeEmailID(EncodeMailboxID(1)); err == nil {
		t.Error("a mailbox id was accepted as an email id")
	}
}

func TestDecodeIDListSeparatesUnknownAndDeduplicates(t *testing.T) {
	ids, byID, unknown := decodeIDList(
		[]string{EncodeEmailID(1), "garbage", EncodeEmailID(1), EncodeEmailID(2)},
		DecodeEmailID)

	if len(ids) != 2 {
		t.Errorf("ids = %v, want the duplicate collapsed", ids)
	}
	if len(unknown) != 1 || unknown[0] != "garbage" {
		t.Errorf("unknown = %v", unknown)
	}
	if byID[1] != EncodeEmailID(1) {
		t.Errorf("wire mapping lost: %v", byID)
	}
}
