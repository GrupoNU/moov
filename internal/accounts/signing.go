package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The export download capability (§2.6): an absolute URL, signed, valid 24 h,
// usable by a browser with no Authorization header.

// DownloadPath is the route a signed URL points at.
const DownloadPath = "/admin/exports/"

// SignedURL builds the absolute signed URL for one export.
//
// origin is the scheme+host the URL is served from. It is part of the SIGNED
// MESSAGE, not just of the string: a signature minted for one host must not
// verify on another, because a multi-host installation serves different
// brands - and, with delegated sign-in, different trust domains - from the
// same binary. §2.6 states the property ("a tampered, foreign-host or expired
// signature answers 404"); binding the origin into the MAC is what makes it
// true rather than aspirational.
func (r *ExportRunner) SignedURL(origin, id string, exp time.Time) string {
	origin = strings.TrimRight(origin, "/")
	sig := r.sign(origin, id, exp)
	q := url.Values{}
	q.Set("exp", strconv.FormatInt(exp.Unix(), 10))
	q.Set("sig", sig)
	return origin + DownloadPath + id + "?" + q.Encode()
}

// sign is the MAC over the three things that must not change: the origin, the
// export id and the expiry. The separator cannot occur in any of them, so two
// different triples can never produce the same message (the canonicalization
// bug that turns an HMAC into a coin flip).
func (r *ExportRunner) sign(origin, id string, exp time.Time) string {
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(origin))
	mac.Write([]byte{0})
	mac.Write([]byte(id))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(exp.Unix(), 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verify checks a presented signature in constant time.
func (r *ExportRunner) verify(origin, id string, exp time.Time, presented string) bool {
	if presented == "" || id == "" {
		return false
	}
	want := r.sign(strings.TrimRight(origin, "/"), id, exp)
	return hmac.Equal([]byte(want), []byte(presented))
}
