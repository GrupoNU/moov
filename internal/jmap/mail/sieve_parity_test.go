package mail

import (
	"reflect"
	"testing"

	"github.com/GrupoNU/moov/internal/sieve"
)

// The FilterRuleValue <-> sieve.Rule mapping is 1:1 by contract
// (sieve_contracts.go promises "a test pins that no field is dropped in
// either direction") — this is that test.

func TestFilterRuleMappingDropsNoField(t *testing.T) {
	yes := true
	in := FilterRuleValue{
		ID: "r1", Name: "n", Type: sieve.RuleFilter, Enabled: true,
		From: []string{"f"}, To: []string{"t"}, Subject: []string{"s"},
		SizeOver: 1, SizeUnder: 2, HasAttachment: &yes,
		MoveTo: "Folder", Labels: []string{"L"}, MarkRead: true, Star: true,
		Forward: "d@x.example", Delete: false, Stop: true,
	}
	out := filterValueFromRule(ruleFromFilterValue(in))
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip lost data:\n in  %+v\n out %+v", in, out)
	}

	// The structural half: a field added to either struct without the
	// mapping following shows up as a count mismatch here, even before
	// anyone writes a value into it.
	//
	// FilterRuleValue flattens Rule's {ID,Name,Type,Enabled} + Criteria(6) +
	// Actions(7) = 17 fields.
	ruleFields := reflect.TypeOf(sieve.Rule{}).NumField() - 2 // Criteria, Actions are structs
	ruleFields += reflect.TypeOf(sieve.Criteria{}).NumField()
	ruleFields += reflect.TypeOf(sieve.Actions{}).NumField()
	if got := reflect.TypeOf(FilterRuleValue{}).NumField(); got != ruleFields {
		t.Errorf("FilterRuleValue has %d fields but the flattened sieve.Rule has %d; "+
			"a field was added without extending the mapping", got, ruleFields)
	}
}
