package main

// The ten query shapes under test. Each is parameterised by account_id ($1)
// plus whatever the shape needs. They are written the way the Moov JMAP layer
// would emit them for an Email/query call: a tsv match, an account scope, an
// ordering, and LIMIT 50 (one screen of results).
//
// Shape #1 is the one that decides the spike: a COMMON word plus
// ORDER BY date DESC LIMIT 50. GIN cannot supply ordering, so the planner must
// either (a) bitmap-scan every match and sort, or (b) walk the date index and
// filter by tsv. Both degrade at 1M messages; this is the classic
// "GIN + order-by-date + LIMIT" pathology.

type shape struct {
	ID      int
	Name    string
	SQL     string
	// Args builds the argument list for a given account.
	ArgsFor func(accountID int, p params) []any
}

// params carries the corpus-derived literals (which word is "common", which is
// "rare", …) so the shapes stay data-driven rather than hardcoded.
type params struct {
	CommonWord   string
	RareWord     string
	TwoWordAND   string // websearch syntax, e.g. "factura vencimiento"
	Phrase       string // websearch syntax with quotes
	PrefixTerm   string // e.g. "factur"
	MailboxID    int
	FromAddr     string
}

const limitClause = " LIMIT 50"

var shapes = []shape{
	{
		ID:   1,
		Name: "common word + date DESC",
		SQL: `SELECT id, date, subject FROM messages
              WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
              ORDER BY date DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.CommonWord} },
	},
	{
		ID:   2,
		Name: "rare word + date DESC",
		SQL: `SELECT id, date, subject FROM messages
              WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
              ORDER BY date DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.RareWord} },
	},
	{
		ID:   3,
		Name: "two-word AND + date DESC",
		SQL: `SELECT id, date, subject FROM messages
              WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
              ORDER BY date DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.TwoWordAND} },
	},
	{
		ID:   4,
		Name: "phrase + date DESC",
		SQL: `SELECT id, date, subject FROM messages
              WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
              ORDER BY date DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.Phrase} },
	},
	{
		ID:   5,
		Name: "prefix (search-as-you-type) + date DESC",
		// websearch_to_tsquery has no prefix syntax; the client-facing
		// search-as-you-type path builds a to_tsquery with :* explicitly.
		SQL: `SELECT id, date, subject FROM messages
              WHERE account_id = $1 AND tsv @@ to_tsquery('simple', $2)
              ORDER BY date DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.PrefixTerm + ":*"} },
	},
	{
		ID:   6,
		Name: "common word + mailbox + last 90 days",
		SQL: `SELECT id, date, subject FROM messages
              WHERE account_id = $1 AND mailbox_id = $3
                AND date >= now() - interval '90 days'
                AND tsv @@ websearch_to_tsquery('simple', $2)
              ORDER BY date DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.CommonWord, p.MailboxID} },
	},
	{
		ID:   7,
		Name: "common word + unread only",
		SQL: `SELECT id, date, subject FROM messages
              WHERE account_id = $1 AND (flags & 1) = 0
                AND tsv @@ websearch_to_tsquery('simple', $2)
              ORDER BY date DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.CommonWord} },
	},
	{
		ID:   8,
		Name: "from-address search (weight B)",
		SQL: `SELECT id, date, subject FROM messages
              WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
              ORDER BY date DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.FromAddr} },
	},
	{
		ID:   9,
		Name: "two-word AND + ts_rank_cd relevance",
		SQL: `SELECT id, date, subject,
                     ts_rank_cd(tsv, websearch_to_tsquery('simple', $2)) AS rank
              FROM messages
              WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
              ORDER BY rank DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.TwoWordAND} },
	},
	{
		ID:   10,
		Name: "exact count for common word",
		SQL: `SELECT count(*) FROM messages
              WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)`,
		ArgsFor: func(a int, p params) []any { return []any{a, p.CommonWord} },
	},
}

// Mitigation shapes for the two remaining failures, #9 (relevance ranking)
// and #10 (exact count). Both fail for the same fundamental reason: unlike the
// date-sorted shapes, they have no LIMIT shortcut — the engine must visit
// EVERY matching row (34,814 of them for "factura vencimiento" in account 1)
// before it can answer. No index fixes that; the query has to ask for less.
//
// #101/#102 bound the candidate set by recency before ranking. This is what
// Gmail effectively does: relevance is computed over a recent window, not the
// whole mailbox. #103/#104 cap the count, which is what Gmail's "many" and
// its "1-50 of many" affordance actually mean.
var mitigationShapes = []shape{
	{
		ID:   101,
		Name: "MITIGATION #9: rank over top-500-by-date candidates",
		SQL: `SELECT id, date, subject, r FROM (
                SELECT id, date, subject,
                       ts_rank_cd(tsv, websearch_to_tsquery('simple', $2)) AS r
                FROM messages
                WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
                ORDER BY date DESC LIMIT 500
              ) s ORDER BY r DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.TwoWordAND} },
	},
	{
		ID:   102,
		Name: "MITIGATION #9: rank over top-200-by-date candidates",
		SQL: `SELECT id, date, subject, r FROM (
                SELECT id, date, subject,
                       ts_rank_cd(tsv, websearch_to_tsquery('simple', $2)) AS r
                FROM messages
                WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
                ORDER BY date DESC LIMIT 200
              ) s ORDER BY r DESC` + limitClause,
		ArgsFor: func(a int, p params) []any { return []any{a, p.TwoWordAND} },
	},
	{
		ID:   103,
		Name: "MITIGATION #10: capped count (LIMIT 1000 — '999+')",
		SQL: `SELECT count(*) FROM (
                SELECT 1 FROM messages
                WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
                LIMIT 1000
              ) s`,
		ArgsFor: func(a int, p params) []any { return []any{a, p.CommonWord} },
	},
	{
		ID:   104,
		Name: "MITIGATION #10: capped count (LIMIT 200 — '199+')",
		SQL: `SELECT count(*) FROM (
                SELECT 1 FROM messages
                WHERE account_id = $1 AND tsv @@ websearch_to_tsquery('simple', $2)
                LIMIT 200
              ) s`,
		ArgsFor: func(a int, p params) []any { return []any{a, p.CommonWord} },
	},
}
