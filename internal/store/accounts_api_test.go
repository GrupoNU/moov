package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Migration 0012 and the store methods behind the accounts API (epic M1),
// against a real PostgreSQL 17. What is proven here is what the Go API cannot
// see: the schema objects exist with the shape the store expects, the
// replay-safety of the migration (moovd runs Migrate on every start), the
// compare-and-set semantics of MarkAccountDeleting, and the survival of an
// export row after its account is gone.

func TestMigration0012SchemaExists(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	columnsOf := func(table string) map[string]string {
		t.Helper()
		rows, err := db.QueryContext(ctx, `
			SELECT column_name, data_type FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = $1`, table)
		if err != nil {
			t.Fatalf("querying information_schema: %v", err)
		}
		defer func() { _ = rows.Close() }()
		got := map[string]string{}
		for rows.Next() {
			var name, kind string
			if err := rows.Scan(&name, &kind); err != nil {
				t.Fatal(err)
			}
			got[name] = kind
		}
		return got
	}

	t.Run("accounts carries the API facts", func(t *testing.T) {
		got := columnsOf("accounts")
		for name, kind := range map[string]string{
			"display_name":            "text",
			"read_only":               "boolean",
			"read_only_since":         "timestamp with time zone",
			"suspended":               "boolean",
			"suspended_at":            "timestamp with time zone",
			"deleting_since":          "timestamp with time zone",
			"last_access_at":          "timestamp with time zone",
			"quota_mb":                "integer",
			"send_per_day":            "integer",
			"recipients_per_message":  "integer",
			"attachment_mb":           "integer",
			"mailcow_app_password_id": "bigint",
		} {
			if got[name] != kind {
				t.Errorf("accounts.%s is %q, want %q", name, got[name], kind)
			}
		}
	})
	for _, table := range []string{"service_accounts", "account_audit", "account_exports"} {
		t.Run(table+" exists", func(t *testing.T) {
			if len(columnsOf(table)) == 0 {
				t.Fatalf("the %s table does not exist", table)
			}
		})
	}
	t.Run("the migration is replay-safe", func(t *testing.T) {
		if err := store.Migrate(ctx, db); err != nil {
			t.Fatalf("second Migrate: %v", err)
		}
	})
}

func TestAccountFactsAndTransitions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	// A pre-0012 shaped account reads as plain active.
	if acct.ReadOnly || acct.Suspended || acct.IsDeleting() || acct.QuotaMB != 0 || acct.SendPerDay != nil {
		t.Fatalf("fresh account carries API facts: %+v", acct)
	}

	spd, rpm := 300, 50
	if err := s.SetAccountFacts(ctx, acct.ID, store.AccountFacts{
		DisplayName: "Expo Diseño 2026", QuotaMB: 2048, SendPerDay: &spd, RecipientsPerMessage: &rpm,
	}); err != nil {
		t.Fatalf("SetAccountFacts: %v", err)
	}
	got, err := s.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Expo Diseño 2026" || got.QuotaMB != 2048 || *got.SendPerDay != 300 ||
		*got.RecipientsPerMessage != 50 || got.AttachmentMB != nil {
		t.Errorf("facts did not round-trip: %+v", got)
	}

	if err := s.SetAccountAppPasswordID(ctx, acct.ID, 42); err != nil {
		t.Fatalf("SetAccountAppPasswordID: %v", err)
	}
	got, _ = s.GetAccount(ctx, acct.ID)
	if got.MailcowAppPasswordID == nil || *got.MailcowAppPasswordID != 42 {
		t.Errorf("app password id = %v, want 42", got.MailcowAppPasswordID)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)

	// Read-only is one-way and keeps its first timestamp.
	if err := s.SetAccountReadOnly(ctx, acct.ID, now); err != nil {
		t.Fatalf("SetAccountReadOnly: %v", err)
	}
	if err := s.SetAccountReadOnly(ctx, acct.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("SetAccountReadOnly again: %v", err)
	}
	got, _ = s.GetAccount(ctx, acct.ID)
	if !got.ReadOnly || got.ReadOnlySince == nil || !got.ReadOnlySince.Equal(now) {
		t.Errorf("read-only = %v since %v, want true since %v", got.ReadOnly, got.ReadOnlySince, now)
	}
	if got.State != store.AccountActive {
		t.Errorf("read-only changed the engine state to %q", got.State)
	}

	// Suspend disables the engine state; resume restores it; read-only is
	// untouched by both (deviation D3).
	if err := s.SetAccountSuspended(ctx, acct.ID, true, now); err != nil {
		t.Fatalf("SetAccountSuspended: %v", err)
	}
	got, _ = s.GetAccount(ctx, acct.ID)
	if !got.Suspended || got.SuspendedAt == nil || got.State != store.AccountDisabled || !got.ReadOnly {
		t.Errorf("after suspend: %+v", got)
	}
	if err := s.SetAccountSuspended(ctx, acct.ID, false, now); err != nil {
		t.Fatalf("SetAccountSuspended(false): %v", err)
	}
	got, _ = s.GetAccount(ctx, acct.ID)
	if got.Suspended || got.SuspendedAt != nil || got.State != store.AccountActive || !got.ReadOnly {
		t.Errorf("after resume: %+v", got)
	}

	// Access touches.
	if err := s.TouchAccountAccess(ctx, acct.ID, now); err != nil {
		t.Fatalf("TouchAccountAccess: %v", err)
	}
	got, _ = s.GetAccount(ctx, acct.ID)
	if got.LastAccessAt == nil || !got.LastAccessAt.Equal(now) {
		t.Errorf("last access = %v, want %v", got.LastAccessAt, now)
	}

	// Deleting is a CAS: the second call finds nothing to mark.
	if err := s.MarkAccountDeleting(ctx, acct.ID, now); err != nil {
		t.Fatalf("MarkAccountDeleting: %v", err)
	}
	if err := s.MarkAccountDeleting(ctx, acct.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second MarkAccountDeleting = %v, want ErrNotFound", err)
	}
	got, _ = s.GetAccount(ctx, acct.ID)
	if !got.IsDeleting() || got.State != store.AccountDisabled || got.CredentialState != store.CredentialRevoked {
		t.Errorf("after MarkAccountDeleting: %+v", got)
	}
	// No transition is allowed on a deleting account.
	if err := s.SetAccountSuspended(ctx, acct.ID, true, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("suspend while deleting = %v, want ErrNotFound", err)
	}
	if err := s.SetAccountReadOnly(ctx, acct.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("readonly while deleting = %v, want ErrNotFound", err)
	}
	deleting, err := s.ListDeletingAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range deleting {
		if a.ID == acct.ID {
			found = true
		}
	}
	if !found {
		t.Error("ListDeletingAccounts does not list the account")
	}
}

func TestServiceAccountCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	secret := fmt.Sprintf("msa1_test-%d", time.Now().UnixNano())
	sum := sha256.Sum256([]byte(secret))
	id := fmt.Sprintf("sa_%d", time.Now().UnixNano())

	sa, err := s.CreateServiceAccount(ctx, store.ServiceAccount{
		ID: id, KeyHash: sum[:], Domain: "eventos.example.test",
		Scopes: []string{store.ScopeAccountsWrite}, Name: "portal",
	})
	if err != nil {
		t.Fatalf("CreateServiceAccount: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(context.Background(), `DELETE FROM service_accounts WHERE id = $1`, id)
	})
	if sa.Revoked() {
		t.Fatal("a new key is revoked")
	}
	if !sa.HasScope(store.ScopeAccountsWrite) || !sa.HasScope(store.ScopeAccountsRead) {
		t.Error("accounts:write must imply accounts:read")
	}
	readOnly := store.ServiceAccount{Scopes: []string{store.ScopeAccountsRead}}
	if readOnly.HasScope(store.ScopeAccountsWrite) {
		t.Error("accounts:read must not imply accounts:write")
	}

	byHash, err := s.GetServiceAccountByHash(ctx, sum[:])
	if err != nil {
		t.Fatalf("GetServiceAccountByHash: %v", err)
	}
	if byHash.ID != id || byHash.Domain != "eventos.example.test" || byHash.Name != "portal" {
		t.Errorf("by hash: %+v", byHash)
	}

	// The domain CHECK refuses an upper-case domain rather than folding it.
	upper := sha256.Sum256([]byte(secret + "-upper"))
	if _, err := s.CreateServiceAccount(ctx, store.ServiceAccount{
		ID: id + "-u", KeyHash: upper[:], Domain: "Eventos.Example.Test", Scopes: []string{"accounts:read"},
	}); err == nil {
		_, _ = s.Pool().Exec(ctx, `DELETE FROM service_accounts WHERE id = $1`, id+"-u")
		t.Error("an upper-case domain was accepted")
	}

	// The same hash twice is refused: a key belongs to exactly one row.
	if _, err := s.CreateServiceAccount(ctx, store.ServiceAccount{
		ID: id + "-dup", KeyHash: sum[:], Domain: "eventos.example.test", Scopes: []string{"accounts:read"},
	}); err == nil {
		_, _ = s.Pool().Exec(ctx, `DELETE FROM service_accounts WHERE id = $1`, id+"-dup")
		t.Error("a duplicate key hash was accepted")
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.TouchServiceAccount(ctx, id, now); err != nil {
		t.Fatalf("TouchServiceAccount: %v", err)
	}
	if err := s.RevokeServiceAccount(ctx, id, now); err != nil {
		t.Fatalf("RevokeServiceAccount: %v", err)
	}
	if err := s.RevokeServiceAccount(ctx, id, now.Add(time.Hour)); err != nil {
		t.Fatalf("second RevokeServiceAccount: %v", err)
	}
	got, err := s.GetServiceAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Revoked() || !got.RevokedAt.Equal(now) {
		t.Errorf("revoked_at = %v, want %v (first revocation wins)", got.RevokedAt, now)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Errorf("last_used_at = %v", got.LastUsedAt)
	}
	if err := s.RevokeServiceAccount(ctx, "sa_does-not-exist", now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revoking a missing key = %v, want ErrNotFound", err)
	}

	list, err := s.ListServiceAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, x := range list {
		if x.ID == id {
			seen = true
		}
	}
	if !seen {
		t.Error("ListServiceAccounts omits the key")
	}
}

func TestAuditLinesOutliveTheAccount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	address := fmt.Sprintf("audited-%d@example.test", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(context.Background(), `DELETE FROM account_audit WHERE address = $1`, address)
	})

	for _, l := range []store.AuditLine{
		{ActorID: "sa_1", ActorName: "portal", Action: "create", Address: address, Result: "ok", RequestID: "r1"},
		{ActorID: "sa_1", ActorName: "portal", Action: "delete", Address: address, Result: "ok", RequestID: "r2", Reason: "event closed"},
	} {
		if err := s.AppendAudit(ctx, l); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	lines, err := s.ListAudit(ctx, address, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Action != "delete" || lines[0].Reason != "event closed" || lines[1].RequestID != "r1" {
		t.Errorf("audit = %+v", lines)
	}
	recreated, err := s.HasAuditFor(ctx, address, "create")
	if err != nil {
		t.Fatal(err)
	}
	if !recreated {
		t.Error("HasAuditFor did not find the create line")
	}
	none, _ := s.HasAuditFor(ctx, address, "readonly")
	if none {
		t.Error("HasAuditFor found a line that was never written")
	}
}

func TestExportLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	id := fmt.Sprintf("exp_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(context.Background(), `DELETE FROM account_exports WHERE id = $1`, id)
	})

	if _, err := s.LatestExport(ctx, acct.Email); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("LatestExport before any = %v, want ErrNotFound", err)
	}
	e, err := s.CreateExport(ctx, id, acct.ID, acct.Email)
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	if e.Status != store.ExportPending || !e.Active() {
		t.Errorf("new export = %+v", e)
	}
	pending, err := s.CountPendingExports(ctx)
	if err != nil || pending < 1 {
		t.Errorf("CountPendingExports = %d, %v", pending, err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	claimed, err := s.ClaimPendingExport(ctx, now)
	if err != nil {
		t.Fatalf("ClaimPendingExport: %v", err)
	}
	// Another test may have queued its own job; claim until ours appears.
	for claimed.ID != id {
		next, err := s.ClaimPendingExport(ctx, now)
		if err != nil {
			t.Fatalf("our export was never claimable: %v", err)
		}
		claimed = next
	}
	if claimed.Status != store.ExportRunning || claimed.StartedAt == nil {
		t.Errorf("claimed = %+v", claimed)
	}
	if err := s.SetExportProgress(ctx, id, 3, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteExport(ctx, id, store.ExportResult{
		Path: "/tmp/x.zip", Bytes: 1234, SHA256: "abc", Messages: 10, Mailboxes: 2,
	}, now); err != nil {
		t.Fatalf("CompleteExport: %v", err)
	}
	latest, err := s.LatestExport(ctx, acct.Email)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != id || latest.Status != store.ExportReady || latest.Messages != 10 ||
		latest.MessagesDone != 10 || latest.Bytes != 1234 || latest.CompletedAt == nil {
		t.Errorf("ready export = %+v", latest)
	}
	// Completing twice is a no-op error: the CAS on status guards it.
	if err := s.CompleteExport(ctx, id, store.ExportResult{}, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second CompleteExport = %v, want ErrNotFound", err)
	}

	expirable, err := s.ListExpirableExports(ctx, now.Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, x := range expirable {
		if x.ID == id {
			seen = true
		}
	}
	if !seen {
		t.Error("the ready export is not listed as expirable past its cutoff")
	}

	// The account goes away; the export row stays (ON DELETE SET NULL) so the
	// signed URL can answer 410 rather than 404.
	if err := s.DeleteAccount(ctx, acct.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if err := s.PurgeExport(ctx, id, now); err != nil {
		t.Fatalf("PurgeExport: %v", err)
	}
	got, err := s.GetExport(ctx, id)
	if err != nil {
		t.Fatalf("GetExport after account deletion: %v", err)
	}
	if got.AccountID != nil || got.Status != store.ExportExpired || got.PurgedAt == nil || got.Path != "" {
		t.Errorf("purged export = %+v", got)
	}
}

func TestForEachAccountMessageOrdersByMailboxThenUID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)
	sent := seedMailbox(t, s, acct.ID, "Sent", store.RoleSent)
	now := time.Now().UTC().Truncate(time.Second)

	msgs := []store.NewMessage{
		{Message: store.Message{AccountID: acct.ID, RawSHA256: seedBlob(t, s, "exp-a"), RawSize: 10, MessageID: "<a@x>", Date: now},
			State: store.MessageState{AccountID: acct.ID, MailboxID: sent.ID, UID: 5, UIDValidity: 1}},
		{Message: store.Message{AccountID: acct.ID, RawSHA256: seedBlob(t, s, "exp-b"), RawSize: 20, MessageID: "<b@x>", Date: now},
			State: store.MessageState{AccountID: acct.ID, MailboxID: inbox.ID, UID: 9, UIDValidity: 1}},
		{Message: store.Message{AccountID: acct.ID, RawSHA256: seedBlob(t, s, "exp-c"), RawSize: 30, MessageID: "<c@x>", Date: now},
			State: store.MessageState{AccountID: acct.ID, MailboxID: inbox.ID, UID: 2, UIDValidity: 1}},
	}
	if _, err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	var order []string
	err := s.ForEachAccountMessage(ctx, acct.ID, func(m store.ExportMessage) error {
		order = append(order, fmt.Sprintf("%s/%d:%s", m.MailboxName, m.UID, m.MessageID))
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachAccountMessage: %v", err)
	}
	want := []string{"INBOX/2:<c@x>", "INBOX/9:<b@x>", "Sent/5:<a@x>"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", order, want)
	}

	summary, err := s.AccountSyncSummary(ctx, acct.ID)
	if err != nil {
		t.Fatalf("AccountSyncSummary: %v", err)
	}
	if summary.Messages != 3 || summary.EverSynced || summary.BreakerOpen {
		t.Errorf("summary = %+v", summary)
	}

	hashes, err := s.AccountBlobHashes(ctx, acct.ID)
	if err != nil || len(hashes) != 3 {
		t.Errorf("AccountBlobHashes = %d, %v", len(hashes), err)
	}
}
