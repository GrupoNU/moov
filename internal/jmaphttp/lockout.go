package jmaphttp

import (
	"sync"
	"time"
)

// lockoutTable is the failed-attempt limiter of arbitration J-A1. It exists
// for one hard operational reason (ADR §4): every Basic-auth validation Moov
// forwards to Dovecot originates, from Mailcow's perspective, at MOOV's
// container IP. Enough forwarded failures and Mailcow's netfilter bans that
// IP — taking down JMAP for every user at once. So failures are throttled
// BEFORE they reach Dovecot, at two scopes:
//
//   - Per client-IP+account: exponential lockout. Failure n locks the pair
//     for min(base·2^(n-1), max); a success clears it. This stops a
//     misconfigured client or a targeted guesser without punishing anyone
//     else. (Behind the same-origin proxy of S1 every client shares the
//     proxy's IP, so the key degenerates to per-account — strictly tighter,
//     never looser.)
//   - Globally: a token bucket of upstream login FAILURES. Per-pair lockout
//     cannot by itself protect Dovecot from an attacker rotating accounts or
//     IPs, and the fail2ban counter upstream is global-per-source-IP too, so
//     the defense must have the same shape. When the bucket is empty, new
//     validations are refused (429) without touching Dovecot; cached
//     credentials keep working, so live users feel nothing.
type lockoutTable struct {
	mu  sync.Mutex
	now func() time.Time

	base    time.Duration
	max     time.Duration
	entries map[string]*lockoutEntry

	// Global failure budget: a token bucket of capacity budgetCapacity that
	// refills completely over budgetWindow. Each upstream login failure
	// consumes one token.
	tokens         float64
	budgetCapacity float64
	refillPerSec   float64
	lastRefill     time.Time
}

type lockoutEntry struct {
	failures int
	until    time.Time
	touched  time.Time
}

// pruneThreshold triggers a sweep of stale entries; pruneIdle is how long an
// expired entry may linger before the sweep removes it.
const (
	pruneThreshold = 4096
	pruneIdle      = time.Hour
)

func newLockoutTable(base, maxLock time.Duration, budget int, window time.Duration, now func() time.Time) *lockoutTable {
	return &lockoutTable{
		now:            now,
		base:           base,
		max:            maxLock,
		entries:        make(map[string]*lockoutEntry),
		tokens:         float64(budget),
		budgetCapacity: float64(budget),
		refillPerSec:   float64(budget) / window.Seconds(),
		lastRefill:     now(),
	}
}

// lockedFor reports how long the key is still locked out; zero means not
// locked.
func (t *lockoutTable) lockedFor(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return 0
	}
	if wait := e.until.Sub(t.now()); wait > 0 {
		return wait
	}
	return 0
}

// recordFailure notes a failed validation for key and returns the lockout it
// now carries.
func (t *lockoutTable) recordFailure(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.maybePrune()

	e, ok := t.entries[key]
	if !ok {
		e = &lockoutEntry{}
		t.entries[key] = e
	}
	e.failures++
	e.touched = t.now()

	lock := t.base
	// Shift with an explicit bound: past ~30 doublings the cap has long won,
	// and an unbounded shift would overflow.
	for i := 1; i < e.failures && i < 31; i++ {
		lock *= 2
		if lock >= t.max {
			lock = t.max
			break
		}
	}
	if lock > t.max {
		lock = t.max
	}
	e.until = t.now().Add(lock)
	return lock
}

// recordSuccess clears the key's failure history.
func (t *lockoutTable) recordSuccess(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// budgetAvailable reports whether the global failure budget still admits a
// validation attempt that might fail upstream.
func (t *lockoutTable) budgetAvailable() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refill()
	return t.tokens >= 1
}

// consumeBudget spends one token for an upstream login failure.
func (t *lockoutTable) consumeBudget() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refill()
	if t.tokens >= 1 {
		t.tokens--
	} else {
		t.tokens = 0
	}
}

// refill tops the bucket up for the time elapsed. Callers hold t.mu.
func (t *lockoutTable) refill() {
	nowT := t.now()
	elapsed := nowT.Sub(t.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	t.tokens += elapsed * t.refillPerSec
	if t.tokens > t.budgetCapacity {
		t.tokens = t.budgetCapacity
	}
	t.lastRefill = nowT
}

// maybePrune sweeps stale entries when the table has grown large. Callers
// hold t.mu. The threshold keeps the sweep O(n) amortized-rare while bounding
// memory against an attacker fabricating keys (e.g. spoofed usernames).
func (t *lockoutTable) maybePrune() {
	if len(t.entries) < pruneThreshold {
		return
	}
	nowT := t.now()
	for k, e := range t.entries {
		if nowT.After(e.until) && nowT.Sub(e.touched) > pruneIdle {
			delete(t.entries, k)
		}
	}
}
