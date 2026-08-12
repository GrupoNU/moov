package jmaphttp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Test doubles for the auth layer: a scriptable validator, an in-memory
// directory, and a manual clock.

type fakeValidator struct {
	mu    sync.Mutex
	calls int
	// valid maps username -> the one accepted password.
	valid map[string]string
	// err, when set, simulates an unavailable authority.
	err error
	// gate, when non-nil, blocks every validation until the channel closes —
	// for the coalescing/concurrency tests.
	gate chan struct{}
}

func (v *fakeValidator) Validate(ctx context.Context, username, password string) (bool, error) {
	v.mu.Lock()
	v.calls++
	gate := v.gate
	err := v.err
	want, ok := v.valid[username]
	v.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if err != nil {
		return false, err
	}
	return ok && password == want, nil
}

func (v *fakeValidator) callCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

type fakeDirectory struct {
	mu       sync.Mutex
	accounts map[string]store.Account
	err      error
}

func (d *fakeDirectory) GetAccountByEmail(_ context.Context, email string) (store.Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return store.Account{}, d.err
	}
	a, ok := d.accounts[email]
	if !ok {
		return store.Account{}, fmt.Errorf("account %q: %w", email, store.ErrNotFound)
	}
	return a, nil
}

func (d *fakeDirectory) put(a store.Account) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.accounts == nil {
		d.accounts = make(map[string]store.Account)
	}
	d.accounts[a.Email] = a
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testAccount is the provisioned account most tests authenticate as.
func testAccount() store.Account {
	return store.Account{
		ID:        7,
		Email:     "user@example.com",
		State:     store.AccountActive,
		UpdatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
}

const testPassword = "app-password-123"

// newTestAuth builds an Authenticator over the fakes with fast test tunings.
func newTestAuth(v *fakeValidator, d *fakeDirectory, clock *fakeClock, mutate func(*AuthConfig)) (*Authenticator, error) {
	cfg := AuthConfig{
		Validator:           v,
		Directory:           d,
		CacheTTL:            10 * time.Minute,
		LockoutBase:         5 * time.Second,
		LockoutMax:          30 * time.Minute,
		GlobalFailureBudget: 1000,
		GlobalFailureWindow: 10 * time.Minute,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:                 clock.Now,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewAuthenticator(cfg)
}
