package mail

import (
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
)

func TestThreadGetReturnsEmailIDsInOrder(t *testing.T) {
	f := newFakeReaders()
	f.threads[testAccountID] = []ThreadRow{
		{ID: EncodeThreadID(1), EmailIDs: []int64{1, 5, 9}},
	}
	d := f.deps()

	got := callGet(t, d.handleThreadGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeThreadID(1)+`"]}`)

	list := array(t, got, "list")
	if len(list) != 1 {
		t.Fatalf("list has %d entries", len(list))
	}
	th, _ := list[0].(map[string]any)
	if th["id"] != EncodeThreadID(1) {
		t.Errorf("id = %v", th["id"])
	}
	ids := array(t, th, "emailIds")
	want := []string{EncodeEmailID(1), EncodeEmailID(5), EncodeEmailID(9)}
	if len(ids) != len(want) {
		t.Fatalf("emailIds = %v", ids)
	}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("emailIds[%d] = %v, want %v", i, ids[i], w)
		}
	}
}

func TestThreadGetNotFound(t *testing.T) {
	f := newFakeReaders()
	f.threads[testAccountID] = []ThreadRow{{ID: EncodeThreadID(1), EmailIDs: []int64{1}}}
	f.threads[otherAccountID] = []ThreadRow{{ID: EncodeThreadID(2), EmailIDs: []int64{2}}}
	d := f.deps()

	got := callGet(t, d.handleThreadGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeThreadID(1)+`","`+
			EncodeThreadID(2)+`","nonsense"]}`)

	if len(array(t, got, "list")) != 1 {
		t.Errorf("list = %#v", got["list"])
	}
	nf := array(t, got, "notFound")
	if len(nf) != 2 {
		t.Fatalf("notFound = %#v, want the foreign thread and the garbage id", nf)
	}
}

// Enumerating every thread would mean threading the whole mailbox, which is
// the unbounded work L2 §4.3 forbids. RFC 8620 §5.1 licenses the refusal.
func TestThreadGetIdsNullIsRefused(t *testing.T) {
	d := newFakeReaders().deps()
	merr := callGetErr(t, d.handleThreadGet, `{"accountId":"`+testAccountJMAPID()+`","ids":null}`)
	if merr.Code != jmap.CodeRequestTooLarge {
		t.Fatalf("code = %s, want requestTooLarge", merr.Code)
	}
}

func TestThreadGetSelectiveProperties(t *testing.T) {
	f := newFakeReaders()
	f.threads[testAccountID] = []ThreadRow{{ID: EncodeThreadID(1), EmailIDs: []int64{1}}}
	d := f.deps()

	got := callGet(t, d.handleThreadGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeThreadID(1)+`"],"properties":["id"]}`)

	th := firstObject(t, got, 0)
	if _, ok := th["emailIds"]; ok {
		t.Error("emailIds was returned but not requested")
	}
	if th["id"] == nil {
		t.Error("id must always be returned")
	}
}

func TestThreadGetUnknownPropertyIsInvalidArguments(t *testing.T) {
	d := newFakeReaders().deps()
	merr := callGetErr(t, d.handleThreadGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["t1"],"properties":["bogus"]}`)
	if merr.Code != jmap.CodeInvalidArguments {
		t.Fatalf("code = %s", merr.Code)
	}
}
