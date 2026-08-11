package crypto

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// key32 is a valid, non-zero 32-byte key rendered in standard base64.
func key32(fill byte) string {
	return EncodeKey(bytes.Repeat([]byte{fill}, KeySize))
}

func TestParseKeyringSingleBareKey(t *testing.T) {
	kr, err := ParseKeyring(key32(0xAA))
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	if kr.PrimaryID() != 1 {
		t.Fatalf("PrimaryID = %d, want the implicit 1", kr.PrimaryID())
	}

	// The parsed key must actually work.
	sealed, err := kr.Seal([]byte("v"), AccountAAD(1))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := kr.Open(sealed, AccountAAD(1)); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func TestParseKeyringExplicitIDs(t *testing.T) {
	kr, err := ParseKeyring("2:" + key32(0xBB) + ",1:" + key32(0xCC))
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	if kr.PrimaryID() != 2 {
		t.Fatalf("PrimaryID = %d, want 2 (the first entry)", kr.PrimaryID())
	}
	if len(kr.IDs()) != 2 {
		t.Fatalf("IDs() = %v, want 2 entries", kr.IDs())
	}
}

func TestParseKeyringWhitespaceAndNewlines(t *testing.T) {
	// A key file written by any editor ends in a newline; a variable pasted by
	// a human may carry spaces. Neither may stop a mail server from starting.
	cases := []string{
		key32(0xDD) + "\n",
		"  " + key32(0xDD) + "  ",
		"2:" + key32(0xDD) + "\n1:" + key32(0xEE) + "\n",
		"2: " + key32(0xDD) + " , 1: " + key32(0xEE),
		"\r\n" + key32(0xDD) + "\r\n",
	}
	for _, in := range cases {
		if _, err := ParseKeyring(in); err != nil {
			t.Errorf("ParseKeyring(%q...): %v", in[:firstN(in, 12)], err)
		}
	}
}

func TestParseKeyringBase64Alphabets(t *testing.T) {
	// An operator copying a key out of a secret manager may get any of these.
	material := bytes.Repeat([]byte{0xFB}, KeySize) // encodes with + and / in std
	for name, enc := range map[string]*base64.Encoding{
		"std":    base64.StdEncoding,
		"rawstd": base64.RawStdEncoding,
		"url":    base64.URLEncoding,
		"rawurl": base64.RawURLEncoding,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseKeyring(enc.EncodeToString(material)); err != nil {
				t.Fatalf("ParseKeyring: %v", err)
			}
		})
	}
}

func TestParseKeyringRefusals(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrNoKey},
		{"whitespace only", "   \n\t ", ErrNoKey},
		{"commas only", ",,,", ErrNoKey},
		{"short key", EncodeKey([]byte("only-sixteen-byt")), ErrKeySize},
		{"long key", EncodeKey(bytes.Repeat([]byte{1}, 33)), ErrKeySize},
		{"all-zero key", EncodeKey(make([]byte, KeySize)), ErrWeakKey},
		{"explicit id 0", "0:" + key32(0x11), ErrZeroKeyID},
		{"duplicate ids", "1:" + key32(0x11) + ",1:" + key32(0x22), ErrDuplicateKeyID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseKeyring(tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseKeyring: got %v, want %v", err, tc.want)
			}
		})
	}

	// These have no sentinel, but must still be refused with a message that
	// explains the fix.
	messageOnly := []struct {
		name, in, wantSubstring string
	}{
		{"not base64", "!!!not base64!!!", "base64"},
		{"id out of range", "256:" + key32(0x11), "1-255"},
		{"non-numeric id", "abc:" + key32(0x11), "key id"},
		// A bare first entry with an explicit second one is reported as the
		// several-bare-keys case: entry 1 is checked before entry 2 is
		// reached. Either message names the real problem.
		{"mixed forms, bare first", key32(0x11) + ",2:" + key32(0x22), "needs"},
		{"mixed forms, id first", "1:" + key32(0x11) + "," + key32(0x22), "one or the other"},
		{"several bare keys", key32(0x11) + "," + key32(0x22), "needs"},
	}
	for _, tc := range messageOnly {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseKeyring(tc.in)
			if err == nil {
				t.Fatal("ParseKeyring accepted an invalid keyring")
			}
			if !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSubstring)
			}
		})
	}
}

func TestLoadKeyringFromEnv(t *testing.T) {
	t.Setenv(EnvMasterKey, key32(0x42))
	t.Setenv(EnvMasterKeyFile, "")

	kr, err := LoadKeyring()
	if err != nil {
		t.Fatalf("LoadKeyring: %v", err)
	}
	if kr.PrimaryID() != 1 {
		t.Fatalf("PrimaryID = %d, want 1", kr.PrimaryID())
	}
}

func TestLoadKeyringFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	// Trailing newline on purpose: this is what a real key file looks like.
	if err := os.WriteFile(path, []byte(key32(0x43)+"\n"), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}

	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvMasterKeyFile, path)

	kr, err := LoadKeyring()
	if err != nil {
		t.Fatalf("LoadKeyring: %v", err)
	}
	sealed, err := kr.Seal([]byte("v"), AccountAAD(1))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := kr.Open(sealed, AccountAAD(1)); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func TestLoadKeyringMultiKeyFile(t *testing.T) {
	// The rotation form: one entry per line.
	path := filepath.Join(t.TempDir(), "master.key")
	body := "2:" + key32(0x44) + "\n1:" + key32(0x45) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvMasterKeyFile, path)

	kr, err := LoadKeyring()
	if err != nil {
		t.Fatalf("LoadKeyring: %v", err)
	}
	if kr.PrimaryID() != 2 {
		t.Fatalf("PrimaryID = %d, want 2", kr.PrimaryID())
	}
	if len(kr.IDs()) != 2 {
		t.Fatalf("IDs() = %v, want 2", kr.IDs())
	}
}

func TestLoadKeyringRefusesMissingKey(t *testing.T) {
	// The headline refusal: no key configured means Moov does not start.
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvMasterKeyFile, "")

	_, err := LoadKeyring()
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("LoadKeyring: got %v, want ErrNoKey", err)
	}
	// The message must tell the operator how to fix it.
	for _, want := range []string{EnvMasterKey, EnvMasterKeyFile, "moovctl key generate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadKeyringRefusesBothSources(t *testing.T) {
	t.Setenv(EnvMasterKey, key32(0x46))
	t.Setenv(EnvMasterKeyFile, filepath.Join(t.TempDir(), "master.key"))

	_, err := LoadKeyring()
	if err == nil {
		t.Fatal("LoadKeyring accepted both sources at once")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error %q does not explain the fix", err)
	}
}

func TestLoadKeyringRefusesZeroKeyFromEnv(t *testing.T) {
	// The specific accident an empty Docker secret or a zeroed buffer causes.
	t.Setenv(EnvMasterKey, EncodeKey(make([]byte, KeySize)))
	t.Setenv(EnvMasterKeyFile, "")

	if _, err := LoadKeyring(); !errors.Is(err, ErrWeakKey) {
		t.Fatalf("LoadKeyring: got %v, want ErrWeakKey", err)
	}
}

func TestLoadKeyringRefusesShortKeyFromEnv(t *testing.T) {
	t.Setenv(EnvMasterKey, EncodeKey([]byte("sixteen-byte-key")))
	t.Setenv(EnvMasterKeyFile, "")

	if _, err := LoadKeyring(); !errors.Is(err, ErrKeySize) {
		t.Fatalf("LoadKeyring: got %v, want ErrKeySize", err)
	}
}

func TestLoadKeyringReportsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.key")
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvMasterKeyFile, path)

	_, err := LoadKeyring()
	if err == nil {
		t.Fatal("LoadKeyring accepted a missing key file")
	}
	// The path is not a secret and naming it is the whole diagnostic value.
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not name the file", err)
	}
}

func TestLoadKeyringDoesNotLeakKeyMaterial(t *testing.T) {
	// A parse failure on the key variable must not echo the value: these
	// errors go to stderr at startup.
	bad := key32(0x47) + "-trailing-garbage"
	t.Setenv(EnvMasterKey, bad)
	t.Setenv(EnvMasterKeyFile, "")

	_, err := LoadKeyring()
	if err == nil {
		t.Fatal("expected a parse failure")
	}
	if strings.Contains(err.Error(), key32(0x47)) {
		t.Fatalf("error leaks key material: %s", err)
	}
}

func firstN(s string, n int) int {
	if len(s) < n {
		return len(s)
	}
	return n
}
