package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Migration 0006 (identities) against a real PostgreSQL 17.
//
// The properties proven here are invisible to the Go API and are exactly the
// ones the running pilot depends on: the schema objects exist, the
// one-default-per-account rule is the DATABASE's and not a convention the Go
// code politely observes, and the BACKFILL reaches accounts that existed
// before the migration ran — which is the case the four pilot accounts are in
// and which can never be re-run once it has happened.

func TestIdentitySchemaExists(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	t.Run("the table exists with the RFC 8621 §6 columns", func(t *testing.T) {
		// Every §6 property that is stored, plus the two bookkeeping columns.
		// mayDelete is deliberately absent: it is derived from is_default, not
		// stored, so a row can never disagree with the refusal the handler
		// makes.
		want := map[string]string{
			"id":             "bigint",
			"account_id":     "bigint",
			"is_default":     "boolean",
			"email":          "text",
			"name":           "text",
			"reply_to":       "jsonb",
			"bcc":            "jsonb",
			"text_signature": "text",
			"html_signature": "text",
			"created_at":     "timestamp with time zone",
			"updated_at":     "timestamp with time zone",
		}
		rows, err := db.QueryContext(ctx, `
			SELECT column_name, data_type FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = 'identities'`)
		if err != nil {
			t.Fatalf("querying information_schema: %v", err)
		}
		defer func() { _ = rows.Close() }()

		got := map[string]string{}
		for rows.Next() {
			var name, typ string
			if err := rows.Scan(&name, &typ); err != nil {
				t.Fatal(err)
			}
			got[name] = typ
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			t.Fatal("the identities table does not exist; migration 0006 did not take effect")
		}
		for col, typ := range want {
			if got[col] == "" {
				t.Errorf("identities.%s is missing", col)
				continue
			}
			if got[col] != typ {
				t.Errorf("identities.%s is %s, want %s", col, got[col], typ)
			}
		}
		for col := range got {
			if want[col] == "" {
				t.Errorf("identities.%s exists but the design does not define it", col)
			}
		}
	})

	t.Run("the signature columns are NOT NULL with the §6 default", func(t *testing.T) {
		// §6 gives textSignature and htmlSignature the default "" — a NULL
		// would make "no signature" and "unset" two different things the JMAP
		// layer would have to collapse anyway.
		for _, col := range []string{"name", "text_signature", "html_signature"} {
			var nullable, def sql.NullString
			err := db.QueryRowContext(ctx, `
				SELECT is_nullable, column_default FROM information_schema.columns
				 WHERE table_schema = 'public' AND table_name = 'identities'
				   AND column_name = $1`, col).Scan(&nullable, &def)
			if err != nil {
				t.Fatalf("querying %s: %v", col, err)
			}
			if nullable.String != "NO" {
				t.Errorf("identities.%s is nullable; §6 gives it the default \"\"", col)
			}
			if !def.Valid || def.String == "" {
				t.Errorf("identities.%s has no default; §6 specifies \"\"", col)
			}
		}
	})

	t.Run("reply_to and bcc ARE nullable", func(t *testing.T) {
		// The mirror of the previous case, and just as deliberate: §6 types
		// these "EmailAddress[]|null (default: null)", so NULL is a value the
		// schema must be able to hold.
		for _, col := range []string{"reply_to", "bcc"} {
			var nullable string
			err := db.QueryRowContext(ctx, `
				SELECT is_nullable FROM information_schema.columns
				 WHERE table_schema = 'public' AND table_name = 'identities'
				   AND column_name = $1`, col).Scan(&nullable)
			if err != nil {
				t.Fatalf("querying %s: %v", col, err)
			}
			if nullable != "YES" {
				t.Errorf("identities.%s is NOT NULL; §6 makes null its default", col)
			}
		}
	})

	t.Run("the indexes exist", func(t *testing.T) {
		for _, idx := range []string{
			"identities_account",         // /get and /changes read by account
			"identities_account_updated", // the state cursor's watermark scan
			"identities_one_default",     // the partial unique index
		} {
			var def string
			err := db.QueryRowContext(ctx, `
				SELECT indexdef FROM pg_indexes
				 WHERE schemaname = 'public' AND indexname = $1`, idx).Scan(&def)
			if err == sql.ErrNoRows {
				t.Errorf("%s does not exist", idx)
				continue
			}
			if err != nil {
				t.Fatalf("querying pg_indexes: %v", err)
			}
		}
	})
}

// The one-default-per-account rule must be enforced by the DATABASE. A
// convention observed only by Go code would be broken by the first bug, and
// two default identities means an ambiguous sender for every submission.
func TestIdentityOneDefaultPerAccountIsEnforced(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	email := fmt.Sprintf("dup-default-%d@example.test", time.Now().UnixNano())
	var accountID int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO accounts (email, imap_host) VALUES ($1, 'dovecot.internal')
		RETURNING id`, email).Scan(&accountID)
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})

	// The migration's backfill already gave this account its default? No — the
	// backfill ran before this row existed, so insert the first one here.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identities (account_id, is_default, email, name)
		VALUES ($1, true, $2, $2)`, accountID, email); err != nil {
		t.Fatalf("inserting the first default identity: %v", err)
	}

	// The second must be refused by the partial unique index.
	_, err = db.ExecContext(ctx, `
		INSERT INTO identities (account_id, is_default, email, name)
		VALUES ($1, true, $2, $2)`, accountID, "second@example.test")
	if err == nil {
		t.Fatal("a second default identity was accepted; the account now has an ambiguous sender")
	}

	// A NON-default identity for the same account is fine — that is the shape
	// alias identities will use, and the partial index must not block it.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identities (account_id, is_default, email, name)
		VALUES ($1, false, $2, $2)`, accountID, "alias@example.test"); err != nil {
		t.Errorf("a non-default identity was refused: %v", err)
	}
}

// The backfill: an account that existed BEFORE migration 0006 must come out of
// it with its identity, because that migration runs exactly once and the four
// pilot accounts are in precisely this position.
//
// It is proven by rolling 0006 back and re-applying it with an account already
// present — which is the real sequence, not a simulation of it.
func TestIdentityBackfillReachesPreExistingAccounts(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	email := fmt.Sprintf("backfill-%d@example.test", time.Now().UnixNano())
	var accountID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO accounts (email, imap_host) VALUES ($1, 'dovecot.internal')
		RETURNING id`, email).Scan(&accountID); err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})

	// Roll back TO 0005: the identities table goes away, the account stays.
	// This is the pre-migration state the pilot's database was in.
	//
	// The target is named rather than counted (MigrateDownTo, not one
	// MigrateDown step) because this test is about migration 0006 specifically.
	// Counting steps made it silently test the head migration instead the day
	// 0007 was added — which is exactly what happened, and is why MigrateDownTo
	// exists.
	if err := store.MigrateDownTo(ctx, db, 5); err != nil {
		t.Fatalf("rolling back to 0005: %v", err)
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_schema = 'public' AND table_name = 'identities')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("the down migration left the identities table behind")
	}

	// Re-apply. The backfill's INSERT ... SELECT must pick the account up.
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("re-applying 0006: %v", err)
	}

	var isDefault bool
	var gotEmail, gotName string
	err := db.QueryRowContext(ctx, `
		SELECT is_default, email, name FROM identities WHERE account_id = $1`,
		accountID).Scan(&isDefault, &gotEmail, &gotName)
	if err == sql.ErrNoRows {
		t.Fatal("the backfill missed a pre-existing account; that account could not send mail")
	}
	if err != nil {
		t.Fatalf("reading the backfilled identity: %v", err)
	}
	if !isDefault {
		t.Error("the backfilled identity is not the default one")
	}
	if gotEmail != email {
		t.Errorf("backfilled email = %q, want the account's own address %q", gotEmail, email)
	}
	// name == email is what the pre-0006 derived identity reported, so no
	// client sees a value change across the deploy.
	if gotName != email {
		t.Errorf("backfilled name = %q, want %q (what handleIdentityGet returned before 0006)", gotName, email)
	}

	// Every OTHER account in the database got one too — the backfill is not
	// allowed to be selective.
	var accounts, withIdentity int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(DISTINCT account_id) FROM identities WHERE is_default`).Scan(&withIdentity); err != nil {
		t.Fatal(err)
	}
	if accounts != withIdentity {
		t.Errorf("%d accounts but only %d have a default identity; the backfill was incomplete",
			accounts, withIdentity)
	}
}

// Re-running the whole migration set must be a no-op, because moovd runs
// migrations on every start (migrate.go) — including the four-account pilot.
func TestIdentityMigrationIsIdempotent(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	var before int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM identities`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := store.Migrate(ctx, db); err != nil {
			t.Fatalf("re-running migrations (pass %d): %v", i, err)
		}
	}
	var after int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM identities`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("re-running migrations changed the identity count: %d -> %d "+
			"(the backfill would duplicate rows on every moovd start)", before, after)
	}
}
