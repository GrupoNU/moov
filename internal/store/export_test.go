package store

// Test-only access to a query builder.
//
// It exists for one assertion that cannot be made any other way: that the SQL
// the repertoire BUILDS reaches the index the plan canaries assert on.
//
// EXPLAINing a hand-written query proves the INDEX works. It does not prove the
// STORE uses it, and the gap between the two is exactly where migration 0008's
// failure mode lives: an expression index applies only when the query repeats
// the indexed expression character for character, so a builder that spells the
// cc or bcc predicate differently falls back to a sequential scan — 145.9 ms
// instead of 1.8 ms — while every hand-written plan test keeps passing.
//
// This forwards to the real builder rather than reimplementing it, which is the
// whole point: a copy would drift in silence, and drift is what is being
// detected.
//
// It lives in an _test.go file, so it is compiled only into the test binary and
// cannot leak into the package's API.

// BuildAccountListSQL exposes AccountListQuery.build to the package's tests.
func BuildAccountListSQL(q AccountListQuery) (string, []any) { return q.build() }

// BuildSearchSQL exposes SearchQuery.build to the package's tests, for the same
// reason: the text path carries the same address predicates.
func BuildSearchSQL(q SearchQuery) (string, []any) { return q.build(false) }
