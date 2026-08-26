package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Paging past the search window (regression, 2026-08-26).
//
// # The defect these pin
//
// The repertoire had no upper date bound and no cursor, so the JMAP layer
// served Email/query's `position` by SLICING one bounded fetch of
// MaxSearchLimit rows. position:200 therefore sliced past the end of a 200-row
// window and returned an empty list — measured against a 626-message INBOX on
// the live pilot — and there was no cursor to page with either: the `before`
// filter was applied AFTER the SQL LIMIT, so it could only shrink a window the
// database had already truncated.
//
// The consequence was not a missing feature but unreachable mail: messages
// 201+ of any folder could not be retrieved by any client, through any
// argument.

// seedPagingCorpus fills one mailbox with n messages carrying a shared needle,
// dated strictly newest-first so the expected page boundaries are unambiguous.
//
// The dates are distinct by construction. Keyset paging is defined over
// (date DESC, id DESC), and a corpus with ties would let a test pass while the
// tiebreaker was broken — which is precisely the bug that duplicates or skips a
// row at a page boundary in production, where equal dates are common (a mailing
// list burst, an import).
func seedPagingCorpus(t *testing.T, s *store.Store, n int) (store.Account, store.Mailbox) {
	t.Helper()
	ctx := context.Background()

	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	now := time.Now().UTC()
	msgs := make([]store.NewMessage, 0, n)
	for i := range n {
		msgs = append(msgs, store.NewMessage{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, fmt.Sprintf("paging-%d-%d", acct.ID, i)),
				RawSize:   200,
				Subject:   fmt.Sprintf("Paginado %s numero %d", needleRare, i),
				FromAddr:  "remitente@example.test",
				ToAddrs:   "destinatario@example.test",
				BodyText:  "cuerpo con el termino " + needleRare + " para paginar",
				Preview:   "cuerpo con el termino",
				// Newest first: message 0 is the most recent.
				Date: now.Add(-time.Duration(i) * time.Minute),
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: inbox.ID,
				UID: int64(i + 1), UIDValidity: 1,
				Flags: store.FlagSeen, ModSeqSeen: int64(i + 1),
			},
		})
	}
	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("seeding %d messages: %v", n, err)
	}
	return acct, inbox
}

// TestSearchPagesPastTheWindow proves the store can walk a result set larger
// than one window, in order, with no duplicates and no gaps — which is what
// makes real paging possible above it. The corpus is deliberately larger than
// MaxSearchLimit, because one that fits in a single window cannot fail this way.
func TestSearchPagesPastTheWindow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const total = store.MaxSearchLimit + 150 // 350: comfortably past one window
	acct, inbox := seedPagingCorpus(t, s, total)

	// Walk the whole set in pages, following the cursor the previous page ends
	// on. This is the loop a client performs; before the fix it could not be
	// written at all.
	const pageSize = 100
	var (
		seen   []int64
		dates  []time.Time
		cursor *store.SearchCursor
		pages  int
	)
	for {
		got, err := s.Search(ctx, store.SearchQuery{
			AccountID: acct.ID,
			Text:      needleRare,
			MailboxID: &inbox.ID,
			Limit:     pageSize,
			After:     cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if len(got) == 0 {
			break
		}
		pages++
		if pages > 20 {
			t.Fatal("paging did not terminate; the cursor is not advancing")
		}
		for _, r := range got {
			seen = append(seen, r.MessageID)
			dates = append(dates, r.Date)
		}
		last := got[len(got)-1]
		cursor = &store.SearchCursor{Date: last.Date, MessageID: last.MessageID}
	}

	// Every message, exactly once.
	if len(seen) != total {
		t.Errorf("paging visited %d messages, want %d "+
			"(messages past the window are unreachable)", len(seen), total)
	}
	unique := make(map[int64]bool, len(seen))
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("message %d was returned on two different pages", id)
		}
		unique[id] = true
	}

	// And in one continuous descending order across page boundaries: a cursor
	// that loses ordering between pages would still return everything while
	// showing the user a shuffled list.
	for i := 1; i < len(dates); i++ {
		if dates[i].After(dates[i-1]) {
			t.Fatalf("ordering broke at index %d across a page boundary", i)
		}
	}
}

// TestSearchCursorIsStableAcrossEqualDates pins the tiebreaker.
//
// Keyset paging on date alone silently breaks when several messages share a
// date — a mailing-list burst, an import, an APPEND loop — by repeating or
// skipping rows at the boundary. The cursor is therefore (date, id), and this
// is the corpus that proves it: every message carries the SAME date, so a
// date-only cursor either loops forever or drops the whole tied block.
func TestSearchCursorIsStableAcrossEqualDates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	const total = 120
	fixed := time.Now().UTC().Truncate(time.Second)
	msgs := make([]store.NewMessage, 0, total)
	for i := range total {
		msgs = append(msgs, store.NewMessage{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, fmt.Sprintf("tie-%d-%d", acct.ID, i)),
				RawSize:   200,
				Subject:   fmt.Sprintf("Empate %s %d", needleRare, i),
				FromAddr:  "remitente@example.test",
				ToAddrs:   "destinatario@example.test",
				BodyText:  "mensaje con " + needleRare + " y fecha identica",
				Preview:   "mensaje con",
				Date:      fixed, // every message, the same instant
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: inbox.ID,
				UID: int64(i + 1), UIDValidity: 1,
				Flags: store.FlagSeen, ModSeqSeen: int64(i + 1),
			},
		})
	}
	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	const pageSize = 25
	seen := map[int64]bool{}
	var cursor *store.SearchCursor
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("paging over equal dates did not terminate: " +
				"the cursor is not breaking ties by id")
		}
		got, err := s.Search(ctx, store.SearchQuery{
			AccountID: acct.ID,
			Text:      needleRare,
			MailboxID: &inbox.ID,
			Limit:     pageSize,
			After:     cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(got) == 0 {
			break
		}
		for _, r := range got {
			if seen[r.MessageID] {
				t.Fatalf("message %d repeated across pages of equal-dated messages", r.MessageID)
			}
			seen[r.MessageID] = true
		}
		last := got[len(got)-1]
		cursor = &store.SearchCursor{Date: last.Date, MessageID: last.MessageID}
	}
	if len(seen) != total {
		t.Errorf("visited %d of %d equal-dated messages", len(seen), total)
	}
}

// TestListMailboxMessagesPagesPastTheWindow pins the same guarantee for the
// folder view, which is the path a client takes when it opens a mailbox with no
// search text — the exact case that failed on the pilot, where an INBOX of 626
// messages served only its newest 200.
func TestListMailboxMessagesPagesPastTheWindow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const total = store.MaxSearchLimit + 120
	acct, inbox := seedPagingCorpus(t, s, total)

	const pageSize = 100
	seen := map[int64]bool{}
	var cursor *store.SearchCursor
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("folder paging did not terminate")
		}
		got, err := s.ListMailboxMessages(ctx, store.MailboxListQuery{
			AccountID: acct.ID,
			MailboxID: inbox.ID,
			Limit:     pageSize,
			After:     cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(got) == 0 {
			break
		}
		for _, r := range got {
			if seen[r.MessageID] {
				t.Fatalf("message %d repeated across folder pages", r.MessageID)
			}
			seen[r.MessageID] = true
		}
		last := got[len(got)-1]
		cursor = &store.SearchCursor{Date: last.Date, MessageID: last.MessageID}
	}
	if len(seen) != total {
		t.Errorf("the folder view reached %d of %d messages", len(seen), total)
	}
}

// TestSearchUntilBoundsTheQueryInSQL pins the upper date bound the JMAP layer's
// `before` filter needs.
//
// Before the fix, `before` was applied in Go AFTER the SQL LIMIT, so it could
// only shrink a window the database had already truncated: a query for mail
// older than a given date returned nothing whenever the newest 200 messages
// were all newer than the bound, even though matches existed.
func TestSearchUntilBoundsTheQueryInSQL(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const total = store.MaxSearchLimit + 100
	acct, inbox := seedPagingCorpus(t, s, total)

	// Everything older than the newest 250 messages: strictly outside the
	// window a single unbounded fetch would have returned.
	newest, err := s.ListMailboxMessages(ctx, store.MailboxListQuery{
		AccountID: acct.ID, MailboxID: inbox.ID, Limit: 1,
	})
	if err != nil || len(newest) == 0 {
		t.Fatalf("reading the newest message: %v", err)
	}
	cutoff := newest[0].Date.Add(-250 * time.Minute)

	got, err := s.Search(ctx, store.SearchQuery{
		AccountID: acct.ID,
		Text:      needleRare,
		MailboxID: &inbox.ID,
		Until:     &cutoff,
		Limit:     50,
	})
	if err != nil {
		t.Fatalf("Search with Until: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("an upper date bound returned nothing: the bound is still being " +
			"applied after the LIMIT instead of in SQL")
	}
	for _, r := range got {
		if !r.Date.Before(cutoff) {
			t.Errorf("message dated %s is not before the %s bound", r.Date, cutoff)
		}
	}
}
