package imap

import (
	"errors"
	"fmt"
	"testing"
)

// serverErr wraps a VERBATIM server response as an error.
//
// It exists to keep the fixtures below byte-identical to what Dovecot actually
// sends. Passing those strings to errors.New directly trips revive's
// error-strings rule, which is a rule about errors this codebase AUTHORS —
// lowercase, no trailing punctuation — and these are not authored, they are
// quoted evidence. Rewording them to satisfy the linter would make the test
// stop testing the thing it exists to pin.
func serverErr(response string) error { return errors.New(response) } //nolint:err113 // quoted server output, not an authored error

// The stale-index matcher.
//
// It matches on response TEXT, which is inherently brittle, so the cases it
// must and must not match are pinned here rather than left to the integration
// suite — where a regression would show up as one confusing failure against a
// live server instead of as a named expectation.
//
// The strings below are verbatim from Dovecot 2.3.21.1, captured by E6's
// integration suite against the real Mailcow.
func TestIsStaleIndexError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil is not stale",
			err:  nil,
			want: false,
		},
		{
			// Verbatim from the E6 run: a folder deleted and recreated while
			// another connection held it open.
			name: "SERVERBUG after a recreated mailbox",
			err: serverErr(`imap: SELECT "MoovE6/x": imap: NO [SERVERBUG] Internal error occurred. ` +
				`Refer to server log for more information. [2026-08-11 01:41:37] (0.001 + 0.000 + 0.001 secs).`),
			want: true,
		},
		{
			// The other shape, and the one an ordinary user produces by
			// deleting a folder in a webmail while Moov has it open.
			name: "mailbox deleted under us",
			err:  serverErr(`imap: SELECT "MoovE6/x": imap: NO Mailbox was deleted under us (0.001 + 0.000 secs).`),
			want: true,
		},
		{
			name: "the NONEXISTENT form of the same condition",
			err:  serverErr(`imap: NO [NONEXISTENT] Mailbox was deleted under us`),
			want: true,
		},
		{
			name: "the explicit index-identity wording",
			err:  serverErr(`imap: NO [SERVERBUG] indexid changed: 1786423293 -> 1786423297`),
			want: true,
		},
		{
			name: "corrupted transaction log under SERVERBUG",
			err:  serverErr(`imap: NO [SERVERBUG] Corrupted transaction log file dovecot.index.log`),
			want: true,
		},
		{
			// The narrowing that matters: a real fault must NOT be retried as
			// if the mailbox had merely been recreated.
			name: "an ordinary NO is not stale",
			err:  serverErr(`imap: SELECT "Nope": imap: NO Mailbox doesn't exist: Nope`),
			want: false,
		},
		{
			name: "authentication failure is not stale",
			err:  serverErr(`imap: NO [AUTHENTICATIONFAILED] Authentication failed.`),
			want: false,
		},
		{
			name: "a network error is not stale",
			err:  errors.New("read tcp 10.0.0.1:993: connection reset by peer"),
			want: false,
		},
		{
			name: "over quota is not stale",
			err:  serverErr(`imap: NO [OVERQUOTA] Quota exceeded`),
			want: false,
		},
		{
			// Wrapped errors must still match: the caller sees this through
			// several layers of fmt.Errorf.
			name: "wrapped stale error",
			err: fmt.Errorf("selecting %q with qresync: %w", "Folder",
				serverErr(`imap: NO Mailbox was deleted under us`)),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStaleIndexError(tc.err); got != tc.want {
				t.Errorf("isStaleIndexError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
