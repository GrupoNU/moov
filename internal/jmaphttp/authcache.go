package jmaphttp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// credentialCache remembers POSITIVE credential validations so that not
// every JMAP request costs a Dovecot IMAP LOGIN (arbitration J-A1: TTL'd
// cache with an invalidation hook). Only "this user/password pair logged in
// successfully" is ever cached; failures are never cached — they feed the
// lockout limiter instead.
//
// # Security design — what is stored, honestly
//
// The cache never stores a password, in any form. Each entry is
// HMAC-SHA256(k, len(user)‖user‖len(pass)‖pass) under a 32-byte key k drawn
// from crypto/rand at process start and never persisted or exported.
//
// What that buys, and what it does not:
//
//   - A memory disclosure (heap dump, core file, swap) that captures the
//     entries but not the key yields nothing: without k the MACs are
//     unforgeable and unbrute-forceable, regardless of password strength.
//   - A disclosure that captures BOTH entries and key allows an offline
//     dictionary attack at one HMAC-SHA256 per guess. A memory-hard KDF
//     (the argon2id the L2 §2.2 draft sketched) would make each guess cost
//     ~100 ms and ~64 MB — but requires a dependency, and this epic is
//     stdlib-only by direction. The exposure window is one process lifetime
//     and at most TTL per entry, and the passwords involved are
//     Mailcow-generated app passwords (high entropy, ADR §4), for which a
//     dictionary attack is not the realistic threat. Recorded as an accepted
//     trade-off; revisit if user-chosen passwords ever reach this path.
//   - Entries are compared with hmac.Equal (constant time). The per-user
//     bucketing means the lookup's timing reveals at most how many DISTINCT
//     valid credentials a user has cached, never anything about their bytes.
//   - The key is per process, so entries are useless across restarts, and a
//     restart is a full invalidation.
type credentialCache struct {
	mu  sync.Mutex
	key []byte
	ttl time.Duration
	now func() time.Time

	// entries buckets by lowercased username so InvalidateUser can find its
	// targets without scanning unrelated users.
	entries map[string][]cacheEntry
}

type cacheEntry struct {
	mac     []byte
	expires time.Time
}

// maxCachedCredentialsPerUser bounds a bucket. A user legitimately has very
// few concurrently-valid credentials (typically one app password); the bound
// exists so a client sending a rotating password can never grow memory.
const maxCachedCredentialsPerUser = 4

func newCredentialCache(ttl time.Duration, now func() time.Time) (*credentialCache, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("jmaphttp: generating the auth cache key: %w", err)
	}
	return &credentialCache{
		key:     key,
		ttl:     ttl,
		now:     now,
		entries: make(map[string][]cacheEntry),
	}, nil
}

// credentialMAC computes the cache tag for a credential pair. The
// length-prefixed encoding makes the (user, pass) → bytes mapping injective:
// ("ab","c") and ("a","bc") can never collide.
func (c *credentialCache) credentialMAC(user, pass string) []byte {
	m := hmac.New(sha256.New, c.key)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(user)))
	m.Write(n[:])
	m.Write([]byte(user))
	binary.BigEndian.PutUint64(n[:], uint64(len(pass)))
	m.Write(n[:])
	m.Write([]byte(pass))
	return m.Sum(nil)
}

// macHex is the coalescing key for in-flight validation of one credential.
func (c *credentialCache) macHex(user, pass string) string {
	return hex.EncodeToString(c.credentialMAC(user, pass))
}

// check reports whether the credential pair has a fresh positive entry,
// pruning the user's expired entries on the way.
func (c *credentialCache) check(user, pass string) bool {
	mac := c.credentialMAC(user, pass)

	c.mu.Lock()
	defer c.mu.Unlock()

	bucket := c.entries[user]
	if len(bucket) == 0 {
		return false
	}
	nowT := c.now()
	kept := bucket[:0]
	found := false
	for _, e := range bucket {
		if nowT.After(e.expires) {
			continue
		}
		kept = append(kept, e)
		if hmac.Equal(e.mac, mac) {
			found = true
		}
	}
	if len(kept) == 0 {
		delete(c.entries, user)
	} else {
		c.entries[user] = kept
	}
	return found
}

// put records a positive validation.
func (c *credentialCache) put(user, pass string) {
	mac := c.credentialMAC(user, pass)

	c.mu.Lock()
	defer c.mu.Unlock()

	bucket := c.entries[user]
	expires := c.now().Add(c.ttl)
	for i, e := range bucket {
		if hmac.Equal(e.mac, mac) {
			bucket[i].expires = expires
			return
		}
	}
	if len(bucket) >= maxCachedCredentialsPerUser {
		// Evict the entry closest to expiry.
		oldest := 0
		for i, e := range bucket {
			if e.expires.Before(bucket[oldest].expires) {
				oldest = i
			}
		}
		bucket[oldest] = cacheEntry{mac: mac, expires: expires}
		c.entries[user] = bucket
		return
	}
	c.entries[user] = append(bucket, cacheEntry{mac: mac, expires: expires})
}

// invalidateUser drops every cached credential of one user — the hook J-A1
// requires, called when an account's credentials are revoked or the account
// is disabled.
func (c *credentialCache) invalidateUser(user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, user)
}

// invalidateAll drops the whole cache.
func (c *credentialCache) invalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string][]cacheEntry)
}
