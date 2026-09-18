package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/GrupoNU/moov/internal/accounts"
	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/jmaphttp"
	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/metrics"
	"github.com/GrupoNU/moov/internal/provision"
	"github.com/GrupoNU/moov/internal/store"
)

// The per-domain accounts API's wiring (epic M1,
// docs/specs/L2-accounts-api-contract.md).
//
// # Why the feature is off unless an operator turns it on
//
// The accounts API is the only part of Moov that can CREATE and DELETE
// mailboxes, and it does it with a Mailcow key that Mailcow does not scope by
// domain (contract §4). That key living in a long-running, network-facing
// process is a new trust boundary, and an operator takes it deliberately by
// setting MOOV_MAILCOW_WRITE_KEY — a different variable from the one moovctl
// reads, which lives on an operator's machine for the length of one command.
//
// With it absent, buildAccountsAPI returns nil and every /admin/accounts
// route answers the generic 404. Not a 501: a prober must not be able to
// learn that the feature exists here and is merely off (§2.1).
//
// # The domain boundary
//
// Because Mailcow's key is unscoped, the ONLY thing standing between one
// consumer and another consumer's mail is Moov's own check that the address
// belongs to the service account's domain. That check is in
// accounts.Service.resolve, before any Mailcow call, and it is pinned by
// test. Nothing in this file may weaken it — in particular, nothing here
// filters or validates domains, because a second place that decided the same
// question is a second place that can disagree with the first.

// exportDirName is the subdirectory of the blob root the export zips live in.
//
// It rides the blob root rather than getting a variable of its own because
// the two have identical operational requirements — a writable, persistent,
// sizeable directory the daemon owns — and an operator who configured one has
// configured the other. A zip here is a temporary artifact with a 7-day life
// (§2.6), not a blob, so it gets its own directory and never the blob tree.
const exportDirName = "exports"

// exportPollInterval is how often the runner looks for queued work.
//
// An export is minutes of work and a portal polls its status every 5-15 s
// (§2.6), so a few seconds of queue latency is invisible; a tighter loop
// would only spend queries on an empty table.
const exportPollInterval = 5 * time.Second

// purgeInterval is how often accounts marked deleting have their rows
// removed.
//
// A minute, not a second: DELETE answers 202 precisely because the purge is
// not request-sized work (§2.4), and the contract promises only that GET
// eventually answers 404 — F0 could not even verify that Mailcow removes the
// maildir promptly. A tight loop would scan a table that is empty almost
// always, for a deadline nobody holds.
const purgeInterval = time.Minute

// accountsComponents holds what the accounts API owns, so the daemon can
// release it in one place.
type accountsComponents struct {
	exports *accounts.ExportRunner
	cancel  context.CancelFunc
	done    chan struct{}
}

// buildAccountsAPI assembles the accounts API over the handles the JMAP
// server already opened, or returns nil when the feature is off.
//
// It takes the store, the blob store and the keyring rather than opening its
// own: the accounts API is a surface OF the JMAP server, not a component
// beside it, and a second pool and a second keyring load would be two more
// things to get out of step for no benefit. The revoker is the seam suspend
// and delete need; see serverRevoker below.
func buildAccountsAPI(
	cfg config.Config,
	st *store.Store,
	blobs *blob.Store,
	keyring *crypto.Keyring,
	revoker accounts.SessionRevoker,
	nudger accounts.SyncNudger,
	m *metrics.Metrics,
	logger *slog.Logger,
) (*jmaphttp.AccountsAPIConfig, *accountsComponents, error) {
	mcCfg, ok, err := mailcow.LoadWriteConfig()
	if err != nil {
		// A write key that is SET but unusable is a configuration error the
		// operator meant to get right, so it stops the daemon rather than
		// silently degrading to "the feature does not exist" — which would be
		// indistinguishable, from outside, from not having set it at all.
		return nil, nil, fmt.Errorf("loading the Mailcow write key: %w", err)
	}
	if !ok {
		logger.Info("accounts api disabled",
			"hint", mailcow.EnvWriteKey+" (or "+mailcow.EnvWriteKeyFile+") enables it")
		return nil, nil, nil
	}

	api, err := mailcow.New(mcCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("building the Mailcow write client: %w", err)
	}
	logger.Info("accounts api enabled", "mailcow", mcCfg.String())

	prov, err := provision.New(provision.Config{
		IMAPHost:       cfg.JMAP.IMAPHost,
		IMAPPort:       cfg.JMAP.IMAPPort,
		IMAPServerName: cfg.JMAP.IMAPServerName,
	}, provision.NewIMAPValidator(logger), api, keyring, st, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("building the provisioner for the accounts api: %w", err)
	}

	// The export runner. Its signing key is DERIVED from the master key with
	// a purpose label, never the master key itself: a download signature and
	// a credential seal must not be produced by the same secret, so that
	// compromising the one that lives in URLs cannot open the one that lives
	// in the database.
	signingKey, err := keyring.Derive(accounts.ExportKeyLabel)
	if err != nil {
		return nil, nil, fmt.Errorf("deriving the export signing key: %w", err)
	}
	exportDir := filepath.Join(cfg.Sync.BlobRoot, exportDirName)
	if err := os.MkdirAll(exportDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("creating the export directory: %w", err)
	}
	runner, err := accounts.NewExportRunner(accounts.ExportConfig{
		Dir:        exportDir,
		SigningKey: signingKey,
		// The BASE URL is left empty on purpose when the deployment did not
		// fix one: a signed URL is bound to the origin it was minted for
		// (accounts/signing.go), and a multi-host installation must mint one
		// per request Host rather than one global answer.
		BaseURL: cfg.JMAP.ExternalURL,
		Logger:  logger,
	}, st, blobs, m)
	if err != nil {
		return nil, nil, fmt.Errorf("building the export runner: %w", err)
	}

	svc, err := accounts.New(accounts.Config{
		MaxQuotaMB: cfg.Accounts.MaxQuotaMB,
		Logger:     logger,
	}, api, st, prov, revoker, runner, m)
	if err != nil {
		return nil, nil, fmt.Errorf("building the accounts service: %w", err)
	}

	// The provisioning nudge (the 2026-09-17 defect). It is nil whenever the
	// sync engine is disabled in this process, which is a supported
	// configuration — a daemon that only serves the API — and the Service
	// handles that by simply not nudging. The engine's own discovery sweep is
	// what guarantees the account is eventually supervised either way; this
	// removes the wait for the person who is already looking at the webmail.
	if nudger != nil {
		svc.SetSyncNudger(nudger)
	}

	runnerCtx, cancel := context.WithCancel(context.Background())
	comp := &accountsComponents{exports: runner, cancel: cancel, done: make(chan struct{}, 2)}
	go func() {
		defer func() { comp.done <- struct{}{} }()
		runner.Run(runnerCtx, exportPollInterval)
	}()
	// The background half of DELETE (§2.4): the rows and blob references of
	// accounts already gone from Mailcow. Its own loop rather than a step
	// inside the export runner's, because the two answer to different clocks
	// and a slow export must never delay a purge.
	go func() {
		defer func() { comp.done <- struct{}{} }()
		runPurge(runnerCtx, svc, logger)
	}()

	return &jmaphttp.AccountsAPIConfig{
		Service: svc,
		Auth:    accounts.NewAuthenticator(st, nil),
		Exports: runner,
	}, comp, nil
}

// shutdown stops the export runner and waits for the job in flight.
//
// A half-written zip is not a correctness problem: the job row is still
// pending or running, and the next daemon claims it and produces the file
// again from the store, which is the authority. Waiting is still the right
// thing — it keeps the directory free of partial files nobody will finish.
func (c *accountsComponents) shutdown(ctx context.Context) {
	if c == nil {
		return
	}
	c.cancel()
	for i := 0; i < cap(c.done); i++ {
		select {
		case <-c.done:
		case <-ctx.Done():
			return
		}
	}
}

// runPurge removes the store rows of deleted accounts until ctx ends.
//
// A failure is logged and retried on the next tick rather than escalated: the
// account is already gone from Mailcow and already invisible to its owner, so
// a row that survives one pass is a cleanup that is late, not a correctness
// problem — and taking the daemon down over it would be the wrong trade by a
// wide margin.
func runPurge(ctx context.Context, svc *accounts.Service, logger *slog.Logger) {
	t := time.NewTicker(purgeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := svc.Purge(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.Error("accounts: the purge pass failed; retrying next tick", "error", err)
				}
				continue
			}
			if n > 0 {
				logger.Info("accounts: purged deleted accounts", "count", n)
			}
		}
	}
}

// serverRevoker implements accounts.SessionRevoker over the two artifacts a
// live session is made of today.
//
// It is the seam contract §2.4 needs for suspend and delete: "every session
// and token revoked". The accounts service must be able to say "this mailbox
// is done" without knowing what KINDS of session exist, which is what lets M2
// add delegated sessions here — one line calling RevokeDelegatedSessions,
// with nothing in internal/accounts to change.
//
// Both halves are invalidated, and in this order for a reason: the token
// cache first, because a minted token outlives the credential it was minted
// from and would otherwise keep working for its full life; then the
// credential cache, so the next request re-validates against Dovecot and
// meets the now-inactive mailbox.
// The server is bound AFTER jmaphttp.New returns, because the accounts
// service is one of New's inputs and the server is its output: the two are
// mutually referential by nature — the API revokes sessions of the server
// that serves it. The cycle is broken in ONE visible place (the bind call,
// three lines below New) rather than spread through a builder.
//
// The pointer is atomic because it is WRITTEN on the startup goroutine and
// READ on whichever request goroutine calls a suspend. That write happens
// before the listener is bound, so no real request can lose the race — but
// "cannot happen in production" is not what -race checks, and an unsynchronised
// pointer here would be a genuine data race regardless of the ordering that
// makes it harmless.
type serverRevoker struct {
	server *atomic.Pointer[jmaphttp.Server]
	auth   *jmaphttp.Authenticator
	store  *store.Store
}

// newServerRevoker builds the seam and the bind that completes it.
func newServerRevoker(auth *jmaphttp.Authenticator, st *store.Store) (accounts.SessionRevoker, func(*jmaphttp.Server)) {
	p := &atomic.Pointer[jmaphttp.Server]{}
	return serverRevoker{server: p, auth: auth, store: st}, p.Store
}

// RevokeAccount invalidates every credential and token of one account.
//
// Idempotent by construction: both underlying calls are cache evictions, and
// evicting what is not there is a no-op — which contract §2.4 requires, since
// suspending an account nobody is using must succeed.
func (r serverRevoker) RevokeAccount(ctx context.Context, accountID int64) error {
	if r.server != nil {
		if srv := r.server.Load(); srv != nil {
			// M1×M2 seam: this revokes the scoped push/blob tokens AND every
			// delegated session of the account. It is safe when delegated
			// sign-in is off — it then only does the token half — so the call
			// is unconditional and suspend cannot silently leave a portal
			// session alive on an installation that configured an issuer.
			//
			// Its error is deliberately not fatal to the revocation: the token
			// caches are already evicted above it, the credential cache is
			// evicted below, and a suspend that fails because ONE of three
			// eviction paths hit a database hiccup would leave the caller
			// believing nothing was revoked when most of it was. The error is
			// returned so the caller can log and retry.
			if err := srv.RevokeDelegatedSessions(ctx, accountID); err != nil {
				return fmt.Errorf("revoking delegated sessions: %w", err)
			}
		}
	}
	if r.auth == nil || r.store == nil {
		return nil
	}
	// The authenticator's cache is keyed by EMAIL (it is what a Basic
	// credential carries), so the row is read to translate. A missing row is
	// not an error here: the account is being deleted, which is one of the
	// two callers, and there is then nothing left to invalidate by name.
	acct, err := r.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil //nolint:nilerr // see above: no row, nothing to evict.
	}
	r.auth.InvalidateUser(acct.Email)
	return nil
}

// The two observer seams, asserted at COMPILE time.
//
// Both are satisfied structurally — internal/metrics knows nothing about
// internal/accounts, which is the point of declaring the interfaces where
// they are used. Structural satisfaction is exactly what a rename can break
// silently: the daemon would still compile, having quietly passed a nil
// interface, and the metric would stop moving with nothing to notice it. The
// assertions turn that into a build failure.
var (
	_ accounts.Observer     = (*metrics.Metrics)(nil)
	_ accounts.PendingGauge = (*metrics.Metrics)(nil)
)
