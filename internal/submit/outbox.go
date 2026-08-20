package submit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The outbox executor: the only component that turns a 'send' intent into an
// SMTP transaction. The state machine and the three rules it enforces are in
// doc.go; this file is their implementation, one persisted transition at a
// time.

// Queue is the executor's slice of the store — exactly the send-intent
// operations of internal/store/submission.go plus the two generics it shares
// with every intent kind. *store.Store satisfies it; the in-memory fake in
// the tests drives the pure state machine without PostgreSQL, while the
// PG-gated suites prove the SKIP LOCKED and crash-recovery properties against
// the real thing.
type Queue interface {
	DueSendAccounts(ctx context.Context) ([]int64, error)
	ClaimDueSendIntents(ctx context.Context, accountID int64, limit int) ([]store.SendIntent, error)
	RecoverInFlightSendIntents(ctx context.Context) ([]store.SendIntent, error)
	MarkSendIntentAccepted(ctx context.Context, id int64, reply string) error
	MarkSendIntentAppended(ctx context.Context, id int64) error
	CompleteIntent(ctx context.Context, id int64) error
	FailIntent(ctx context.Context, id int64, message string, retryAt *time.Time) error
	GetAccount(ctx context.Context, id int64) (store.Account, error)
}

// Transport performs one SMTP transaction for one account. cmd/moovd
// implements it over Send() plus the credential keyring — the same seam shape
// as the sync engine's Connector, and for the same reason: an executor that
// could decrypt credentials would be an executor that has to be trusted with
// them.
//
// onAccepted carries doc.go rule 1 through the seam: the implementation MUST
// invoke it between reading the 250 and doing anything else, exactly as
// Send() does.
type Transport interface {
	Send(ctx context.Context, account store.Account, env Envelope, msg io.Reader, onAccepted func(reply string) error) (Result, error)
}

// SentMailbox is the post-send \Sent operation set, implemented by the sync
// engine's write executor (internal/sync/append.go). It is the only IMAP the
// outbox can cause, and it goes through the same layered seam every IMAP
// operation does.
type SentMailbox interface {
	// AppendToSent appends the transmitted bytes unless a message with the
	// same Message-ID is already there; deduped reports which happened.
	AppendToSent(ctx context.Context, accountID int64, raw []byte, messageID string) (deduped bool, err error)

	// SentContainsMessageID is the recovery probe — the second net of ADR §4.
	SentContainsMessageID(ctx context.Context, accountID int64, messageID string) (bool, error)
}

// RawSource opens the draft's raw bytes. mail.Adapter's RawMessage satisfies
// it by construction.
type RawSource interface {
	RawMessage(ctx context.Context, accountID, messageID int64) (io.ReadCloser, error)
}

// Notifier receives account-changed notifications. *sync.Broker satisfies it,
// nil-safety included on the caller's side (notify()).
type Notifier interface {
	Notify(accountID int64)
}

// Observer receives one call per TERMINAL submission outcome, so the daemon
// can count sends and failures without this package importing the metrics
// exporter — the same seam shape as Notifier, and for the same reason: an
// executor whose correctness depends on a metrics registry being present is an
// executor that cannot be tested without one.
//
// result is one of the metrics package's submission result constants ("sent",
// "failed"); cancellation is not observable here because it happens in the
// JMAP layer, on a row this executor never claims.
type Observer interface {
	SubmissionFinished(result string)
}

// The terminal results reported through Observer. They mirror the metrics
// package's constants, which this package does not import; a test in cmd/moovd
// pins the two sets together.
const (
	ResultSent   = "sent"
	ResultFailed = "failed"
)

// IntentEnvelope is the submission payload the JMAP layer stores on the
// intent row and the executor reads back: the RFC 8621 §7.1 envelope, already
// validated and derived at create time. The executor re-validates nothing —
// by the time a row is claimable, its envelope was checked against the
// account (forbiddenMailFrom et al.) and frozen.
type IntentEnvelope struct {
	IdentityID string   `json:"identityId"`
	MailFrom   string   `json:"mailFrom"`
	RcptTo     []string `json:"rcptTo"`
}

// Options configures an Outbox.
type Options struct {
	// PollInterval is how often the queue is scanned for due work. Default
	// 1s — the undo window's resolution is seconds, so a sub-second poll
	// would only cost queries.
	PollInterval time.Duration

	// ClaimBatch bounds one claim. Default 10.
	ClaimBatch int

	// MaxAttempts caps transient pre-acceptance retries; reaching it turns
	// the failure permanent. Default 8, which with the default backoff spans
	// roughly an hour of trying.
	MaxAttempts int

	// RetryBase and RetryMax shape the exponential backoff between transient
	// attempts. Defaults 30s and 10m.
	RetryBase time.Duration
	RetryMax  time.Duration

	// Logger receives structured diagnostics. Default slog.Default().
	Logger *slog.Logger

	// Clock is injectable for tests. Default time.Now.
	Clock func() time.Time

	// Notifier, when set, is told after every observable transition so SSE
	// clients see EmailSubmission (and Email) state move.
	Notifier Notifier

	// Observer, when set, is told once per terminal outcome (W4b metrics).
	Observer Observer
}

func (o Options) withDefaults() Options {
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.ClaimBatch <= 0 {
		o.ClaimBatch = 10
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 8
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 30 * time.Second
	}
	if o.RetryMax <= 0 {
		o.RetryMax = 10 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o
}

// Outbox drives 'send' intents to completion. Construct with NewOutbox, run
// with Run; one instance serves every account.
type Outbox struct {
	queue     Queue
	transport Transport
	sent      SentMailbox
	raws      RawSource
	opts      Options
	log       *slog.Logger

	// executing guards against the same intent id being executed twice within
	// one process — the claim protects across processes and goroutines at the
	// database, this protects the recovery pass overlapping a poll that
	// re-queued a post-send retry.
	mu        sync.Mutex
	executing map[int64]bool

	// persistRetryBase is the first in-place acceptance-persist retry delay
	// (persistAcceptance); shrunk by tests so the six-attempt ladder does not
	// cost the suite half a minute. Production keeps the default.
	persistRetryBase time.Duration
}

// NewOutbox builds the executor.
func NewOutbox(queue Queue, transport Transport, sent SentMailbox, raws RawSource, opts Options) (*Outbox, error) {
	if queue == nil || transport == nil || sent == nil || raws == nil {
		return nil, errors.New("submit: NewOutbox requires a queue, a transport, a sent mailbox and a raw source")
	}
	opts = opts.withDefaults()
	return &Outbox{
		queue:            queue,
		transport:        transport,
		sent:             sent,
		raws:             raws,
		opts:             opts,
		log:              opts.Logger.With("component", "outbox"),
		executing:        map[int64]bool{},
		persistRetryBase: 500 * time.Millisecond,
	}, nil
}

// Run executes the outbox until ctx ends: one recovery pass (W-A3: "el
// arranque reconcilia ... antes de tocar nada pendiente"), then the poll loop.
// It always returns ctx's error; the outbox has no failure that should stop
// the daemon — a broken store surfaces per-tick in the log and heals when the
// store does.
func (o *Outbox) Run(ctx context.Context) error {
	o.recover(ctx)

	ticker := time.NewTicker(o.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.runOnce(ctx)
		}
	}
}

// runOnce claims and executes everything currently due. Exported through the
// tests (and useful for a deterministic tick) rather than public API.
func (o *Outbox) runOnce(ctx context.Context) {
	accounts, err := o.queue.DueSendAccounts(ctx)
	if err != nil {
		o.log.Warn("scanning due send accounts failed", "error", err)
		return
	}
	for _, accountID := range accounts {
		if ctx.Err() != nil {
			return
		}
		intents, err := o.queue.ClaimDueSendIntents(ctx, accountID, o.opts.ClaimBatch)
		if err != nil {
			o.log.Warn("claiming send intents failed", "account_id", accountID, "error", err)
			continue
		}
		for _, in := range intents {
			o.execute(ctx, in, false)
		}
	}
}

// recover handles the rows a previous process left in_flight — the startup
// half of the crash matrix. recovered=true routes each through the second-net
// probe before any transmission is considered.
func (o *Outbox) recover(ctx context.Context) {
	intents, err := o.queue.RecoverInFlightSendIntents(ctx)
	if err != nil {
		o.log.Error("recovering in-flight send intents failed", "error", err)
		return
	}
	if len(intents) > 0 {
		o.log.Info("recovering stranded send intents", "count", len(intents))
	}
	for _, in := range intents {
		o.execute(ctx, in, true)
	}
}

// execute drives one claimed intent as far as it can go in this pass.
//
// The FIRST branch is rule 2: acceptance decides everything. A row that has
// been accepted goes only forward (post-send steps); a row that has not may
// be transmitted — after the recovery probe, when this execution is not the
// row's first attempt.
func (o *Outbox) execute(ctx context.Context, in store.SendIntent, recovered bool) {
	if !o.begin(in.ID) {
		return
	}
	defer o.end(in.ID)

	log := o.log.With("intent_id", in.ID, "account_id", in.AccountID,
		"email_id", in.EmailID, "attempt", in.Attempts)

	if !in.Accepted() {
		if !o.transmit(ctx, &in, recovered, log) {
			return // failed or re-queued; transitions already persisted
		}
	}

	o.postSend(ctx, &in, log)
}

// transmit performs the pre-acceptance half: the recovery probe, the SMTP
// transaction, and the acceptance persist. It reports whether the intent is
// now accepted and execution should continue to the post-send steps.
func (o *Outbox) transmit(ctx context.Context, in *store.SendIntent, recovered bool, log *slog.Logger) bool {
	// The second net (ADR §4): any RE-execution — a recovered in_flight row,
	// or a retry after a transient — first asks whether the message already
	// reached \Sent under its Message-ID. That catches both a server-side
	// auto-saved copy and our own crash after the APPEND; a hit means the
	// message was sent, whatever the row says.
	if recovered || in.Attempts > 1 {
		found, err := o.sent.SentContainsMessageID(ctx, in.AccountID, in.MessageRFCID)
		if err != nil {
			log.Warn("the Sent dedupe probe failed; deferring the intent rather than risking a resend", "error", err)
			o.requeue(ctx, in, "sent-probe failed: "+err.Error(), log)
			return false
		}
		if found {
			log.Warn("recovered intent's message already present in Sent; marking accepted without transmitting",
				"message_rfc_id", in.MessageRFCID)
			reply := "recovered: message found in Sent by Message-ID"
			if err := o.persistAcceptance(ctx, in.ID, reply, log); err != nil {
				o.requeue(ctx, in, "persisting recovered acceptance: "+err.Error(), log)
				return false
			}
			o.markAccepted(in, reply)
			return true
		}
	}

	raw, env, err := o.prepare(ctx, *in)
	if err != nil {
		// A submission whose draft is gone or whose payload does not parse
		// can never succeed: permanent, visible, no retry.
		o.failPermanently(ctx, in, "preparing the message: "+err.Error(), log)
		return false
	}

	account, err := o.queue.GetAccount(ctx, in.AccountID)
	if err != nil {
		o.requeue(ctx, in, "loading the account: "+err.Error(), log)
		return false
	}

	env.Size = int64(len(raw))
	var acceptedReply string
	_, err = o.transport.Send(ctx, account, env, bytes.NewReader(raw), func(reply string) error {
		// Rule 1, at the moment it applies: the 250 has just been read and
		// NOTHING else has happened. This callback persists it; the residual
		// window of the whole design is this one statement (doc.go).
		//
		// WithoutCancel is deliberate: once the server said 250, a shutdown
		// signal racing this exact instant must not be able to abort the one
		// write that prevents a double send. The statement is a single
		// UPDATE; it finishes in milliseconds or fails on its own merits.
		acceptedReply = reply
		return o.queue.MarkSendIntentAccepted(context.WithoutCancel(ctx), in.ID, reply)
	})

	switch {
	case err == nil:
		o.markAccepted(in, acceptedReply)
		o.notify(in.AccountID)
		return true

	case errors.Is(err, ErrAcceptedUnrecorded):
		// The message IS sent; only the recording failed (the store hiccuped
		// at the worst instant). Retry the persist in place — never the
		// transmission.
		log.Error("SMTP accepted the message but persisting the acceptance failed; retrying the persist",
			"error", err)
		var au *AcceptedUnrecordedError
		reply := acceptedReply
		if errors.As(err, &au) && au.Reply != "" {
			reply = au.Reply
		}
		if perr := o.persistAcceptance(ctx, in.ID, reply, log); perr != nil {
			// The store stayed down. The row remains in_flight — it is NOT
			// re-queued, because re-queueing an unrecorded acceptance is
			// scheduling a double send. The next startup's recovery pass
			// re-probes \Sent first; doc.go documents the residue.
			log.Error("CRITICAL: acceptance could not be persisted; intent left in_flight for recovery",
				"error", perr, "message_rfc_id", in.MessageRFCID)
			return false
		}
		o.markAccepted(in, reply)
		o.notify(in.AccountID)
		return true

	case IsPermanent(err):
		// RFC 5321 §4.2.1 5yz: the server said never. Final undoStatus,
		// visible deliveryStatus, no retry (rule 2's permanent branch).
		o.failPermanently(ctx, in, err.Error(), log)
		return false

	default:
		// Transient: connection refused, 4yz, timeout. Nothing was accepted,
		// so retrying is safe — with backoff, up to the attempt cap.
		if in.Attempts >= o.opts.MaxAttempts {
			o.failPermanently(ctx, in,
				fmt.Sprintf("giving up after %d attempts; last error: %v", in.Attempts, err), log)
			return false
		}
		o.requeue(ctx, in, err.Error(), log)
		return false
	}
}

// postSend completes the phases that follow acceptance: the \Sent copy, then
// done. Failures here re-queue ONLY these steps — the accepted_at guard in
// execute() is what makes that claim structural.
func (o *Outbox) postSend(ctx context.Context, in *store.SendIntent, log *slog.Logger) {
	if in.AppendedAt == nil {
		raw, _, err := o.prepare(ctx, *in)
		switch {
		case err != nil:
			// The draft blob disappeared between the send and the copy (an
			// aggressive expunge). The message was sent; a Sent copy is no
			// longer derivable. Recording the phase as done with the reason in
			// the log beats retrying forever toward bytes that cannot return.
			log.Warn("transmitted bytes are no longer derivable; skipping the Sent copy", "error", err)
		default:
			deduped, aerr := o.sent.AppendToSent(ctx, in.AccountID, raw, in.MessageRFCID)
			if aerr != nil {
				// ADR §4: "fallo del APPEND = warning + reintento del APPEND
				// solo, jamás re-envío".
				log.Warn("appending the Sent copy failed; will retry the append only", "error", aerr)
				o.requeue(ctx, in, "sent-append: "+aerr.Error(), log)
				return
			}
			if deduped {
				log.Info("Sent already holds this Message-ID; append skipped", "message_rfc_id", in.MessageRFCID)
			}
		}
		if err := o.queue.MarkSendIntentAppended(ctx, in.ID); err != nil {
			log.Warn("stamping the append phase failed; will retry", "error", err)
			o.requeue(ctx, in, "stamping append: "+err.Error(), log)
			return
		}
		now := o.opts.Clock()
		in.AppendedAt = &now
	}

	if err := o.queue.CompleteIntent(ctx, in.ID); err != nil {
		log.Warn("completing the intent failed; will retry", "error", err)
		o.requeue(ctx, in, "completing: "+err.Error(), log)
		return
	}
	log.Info("submission complete", "message_rfc_id", in.MessageRFCID)
	o.notify(in.AccountID)
}

// prepare derives the transmitted bytes and the envelope from the intent —
// the deterministic function message.go documents.
func (o *Outbox) prepare(ctx context.Context, in store.SendIntent) ([]byte, Envelope, error) {
	var env IntentEnvelope
	if err := json.Unmarshal(in.Payload, &env); err != nil {
		return nil, Envelope{}, fmt.Errorf("decoding the intent payload: %w", err)
	}
	if env.MailFrom == "" || len(env.RcptTo) == 0 {
		return nil, Envelope{}, errors.New("the intent payload holds no usable envelope")
	}

	rc, err := o.raws.RawMessage(ctx, in.AccountID, in.EmailID)
	if err != nil {
		return nil, Envelope{}, fmt.Errorf("opening the draft: %w", err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, Envelope{}, fmt.Errorf("reading the draft: %w", err)
	}

	prepared := PrepareTransmission(raw, in.MessageRFCID, in.CreatedAt)
	return prepared, Envelope{MailFrom: env.MailFrom, RcptTo: env.RcptTo}, nil
}

// persistAcceptance retries MarkSendIntentAccepted with a short in-place
// backoff: an acceptance that exists only in memory is the one state this
// executor may not shrug at. The persist itself runs on an uncancelable
// context for the same reason the callback's does; only the waits between
// attempts honor shutdown.
func (o *Outbox) persistAcceptance(ctx context.Context, id int64, reply string, log *slog.Logger) error {
	var last error
	for attempt, delay := 0, o.persistRetryBase; attempt < 6; attempt, delay = attempt+1, delay*2 {
		err := o.queue.MarkSendIntentAccepted(context.WithoutCancel(ctx), id, reply)
		if err == nil {
			return nil
		}
		last = err
		log.Warn("persisting acceptance failed; retrying in place", "attempt", attempt+1, "error", err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return errors.Join(last, ctx.Err())
		}
	}
	return last
}

// requeue returns a transient failure to the queue with exponential backoff.
// The row's phase columns survive, so a post-acceptance requeue retries only
// the post-send steps.
func (o *Outbox) requeue(ctx context.Context, in *store.SendIntent, msg string, log *slog.Logger) {
	retryAt := o.opts.Clock().Add(o.backoff(in.Attempts))
	if err := o.queue.FailIntent(ctx, in.ID, msg, &retryAt); err != nil {
		log.Error("re-queueing the intent failed; it stays in_flight for recovery", "error", err)
		return
	}
	log.Info("intent re-queued", "retry_at", retryAt, "reason", msg)
	o.notify(in.AccountID)
}

// failPermanently marks the intent failed for good: undoStatus becomes
// "final" with a visible deliveryStatus, and the executor never touches the
// row again.
func (o *Outbox) failPermanently(ctx context.Context, in *store.SendIntent, msg string, log *slog.Logger) {
	if err := o.queue.FailIntent(ctx, in.ID, msg, nil); err != nil {
		log.Error("marking the intent permanently failed failed; it stays in_flight for recovery", "error", err)
		return
	}
	log.Warn("submission permanently failed", "reason", msg)
	o.observe(ResultFailed)
	o.notify(in.AccountID)
}

// backoff is the transient retry delay for a row on its nth attempt, with
// jitter so a burst of failures does not resynchronize into a thundering
// herd against a recovering Postfix.
func (o *Outbox) backoff(attempts int) time.Duration {
	d := o.opts.RetryBase
	for i := 1; i < attempts && d < o.opts.RetryMax; i++ {
		d *= 2
	}
	if d > o.opts.RetryMax {
		d = o.opts.RetryMax
	}
	// ±20% jitter.
	frac := 0.8 + 0.4*rand.Float64() // #nosec G404 -- jitter, not cryptography
	return time.Duration(float64(d) * frac)
}

// markAccepted stamps the in-memory row after the acceptance was persisted.
//
// This is where "sent" is counted, and it is the right place precisely because
// EVERY acceptance path funnels through it: the ordinary 250, the recovered
// ErrAcceptedUnrecorded persist, and the recovery probe that found the message
// already in \Sent. Counting at the transport instead would miss the last two
// and would count an acceptance the store never recorded.
//
// It runs once per intent because execute() branches on Accepted() first: a
// row that comes back for its post-send steps never re-enters transmit().
func (o *Outbox) markAccepted(in *store.SendIntent, reply string) {
	now := o.opts.Clock()
	in.AcceptedAt = &now
	in.AcceptedReply = reply
	o.observe(ResultSent)
}

func (o *Outbox) notify(accountID int64) {
	if o.opts.Notifier != nil {
		o.opts.Notifier.Notify(accountID)
	}
}

func (o *Outbox) observe(result string) {
	if o.opts.Observer != nil {
		o.opts.Observer.SubmissionFinished(result)
	}
}

func (o *Outbox) begin(id int64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.executing[id] {
		return false
	}
	o.executing[id] = true
	return true
}

func (o *Outbox) end(id int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.executing, id)
}
