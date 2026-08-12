package jmap

import (
	"reflect"
	"testing"
)

func TestDefaultLimitsMeetRFCSuggestedMinimums(t *testing.T) {
	l := DefaultLimits()
	// RFC 8620 §2 suggested minimums, so clients tuned to them never hit an
	// artificial wall.
	if l.MaxSizeUpload < 50_000_000 ||
		l.MaxConcurrentUpload < 4 ||
		l.MaxSizeRequest < 10_000_000 ||
		l.MaxConcurrentRequests < 4 ||
		l.MaxCallsInRequest < 16 ||
		l.MaxObjectsInGet < 500 ||
		l.MaxObjectsInSet < 500 {
		t.Fatalf("defaults below RFC 8620 §2 suggested minimums: %+v", l)
	}
}

func TestCoreCapabilityAdvertisesEveryLimitField(t *testing.T) {
	// The capability object must be built from the Limits struct and cover
	// every field: a limit added to the struct without being advertised (or
	// advertised from another source) fails here.
	l := Limits{
		MaxSizeUpload:         1,
		MaxConcurrentUpload:   2,
		MaxSizeRequest:        3,
		MaxConcurrentRequests: 4,
		MaxCallsInRequest:     5,
		MaxObjectsInGet:       6,
		MaxObjectsInSet:       7,
	}
	capability := l.CoreCapability()

	wireName := map[string]string{
		"MaxSizeUpload":         "maxSizeUpload",
		"MaxConcurrentUpload":   "maxConcurrentUpload",
		"MaxSizeRequest":        "maxSizeRequest",
		"MaxConcurrentRequests": "maxConcurrentRequests",
		"MaxCallsInRequest":     "maxCallsInRequest",
		"MaxObjectsInGet":       "maxObjectsInGet",
		"MaxObjectsInSet":       "maxObjectsInSet",
	}

	typ := reflect.TypeOf(l)
	val := reflect.ValueOf(l)
	for i := range typ.NumField() {
		field := typ.Field(i)
		wire, known := wireName[field.Name]
		if !known {
			t.Fatalf("Limits field %s has no wire mapping in this test: add it AND its enforcement", field.Name)
		}
		advertised, ok := capability[wire]
		if !ok {
			t.Fatalf("limit %s is declared but not advertised in the core capability", field.Name)
		}
		var want int64
		switch v := val.Field(i).Interface().(type) {
		case int64:
			want = v
		case int:
			want = int64(v)
		default:
			t.Fatalf("Limits field %s has unhandled type %T", field.Name, v)
		}
		var got int64
		switch v := advertised.(type) {
		case int64:
			got = v
		case int:
			got = int64(v)
		default:
			t.Fatalf("capability %s has unhandled type %T", wire, v)
		}
		if got != want {
			t.Fatalf("capability %s advertises %d, struct declares %d", wire, got, want)
		}
	}

	if _, ok := capability["collationAlgorithms"]; !ok {
		t.Fatal("collationAlgorithms missing (required by RFC 8620 §2)")
	}
}

func TestCheckObjectsInGet(t *testing.T) {
	l := Limits{MaxObjectsInGet: 3}
	if merr := l.CheckObjectsInGet(3); merr != nil {
		t.Fatalf("at-limit rejected: %v", merr)
	}
	merr := l.CheckObjectsInGet(4)
	if merr == nil || merr.Code != CodeRequestTooLarge {
		t.Fatalf("got %v, want requestTooLarge", merr)
	}
}

func TestCheckObjectsInSet(t *testing.T) {
	l := Limits{MaxObjectsInSet: 2}
	if merr := l.CheckObjectsInSet(2); merr != nil {
		t.Fatalf("at-limit rejected: %v", merr)
	}
	merr := l.CheckObjectsInSet(3)
	if merr == nil || merr.Code != CodeRequestTooLarge {
		t.Fatalf("got %v, want requestTooLarge", merr)
	}
}
