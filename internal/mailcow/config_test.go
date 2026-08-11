package mailcow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeDefaults(t *testing.T) {
	c, err := Config{BaseURL: "https://mail.example.com", APIKey: "K"}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if c.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %s, want %s", c.Timeout, DefaultTimeout)
	}
	if c.AppNamePrefix != DefaultAppNamePrefix {
		t.Errorf("AppNamePrefix = %q, want %q", c.AppNamePrefix, DefaultAppNamePrefix)
	}
	// The S1 H5 default: a zero-valued config must force IPv4, because the
	// operator who forgot the field should get the behavior that works.
	if !c.ForceIPv4 {
		t.Error("ForceIPv4 = false by default; S1 H5 requires IPv4 to be forced unless opted out")
	}
	if c.InsecureSkipVerify {
		t.Error("InsecureSkipVerify defaults to true, which must never happen")
	}
}

func TestNormalizeAPIPathSuffix(t *testing.T) {
	// Both forms must work, so an operator never has to guess.
	cases := []struct{ in, want string }{
		{"https://mail.example.com", "https://mail.example.com/api/v1"},
		{"https://mail.example.com/", "https://mail.example.com/api/v1"},
		{"https://mail.example.com/api/v1", "https://mail.example.com/api/v1"},
		{"https://mail.example.com/api/v1/", "https://mail.example.com/api/v1"},
		{"http://172.22.1.12", "http://172.22.1.12/api/v1"},
		{"https://nginx:443", "https://nginx:443/api/v1"},
	}
	for _, tc := range cases {
		c, err := Config{BaseURL: tc.in, APIKey: "K"}.Normalize()
		if err != nil {
			t.Errorf("Normalize(%q): %v", tc.in, err)
			continue
		}
		if c.BaseURL != tc.want {
			t.Errorf("Normalize(%q).BaseURL = %q, want %q", tc.in, c.BaseURL, tc.want)
		}
	}
}

func TestNormalizeRefusals(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"no base url", Config{APIKey: "K"}},
		{"no api key", Config{BaseURL: "https://mail.example.com"}},
		{"blank api key", Config{BaseURL: "https://mail.example.com", APIKey: "   "}},
		{"bad scheme", Config{BaseURL: "ftp://mail.example.com", APIKey: "K"}},
		{"no scheme", Config{BaseURL: "mail.example.com", APIKey: "K"}},
		{"no host", Config{BaseURL: "https://", APIKey: "K"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.Normalize(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("got %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestNormalizeForceIPv6Optout(t *testing.T) {
	c, err := Config{BaseURL: "https://m.example.com", APIKey: "K", ForceIPv6Allowed: true}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if c.ForceIPv4 {
		t.Error("ForceIPv6Allowed did not turn off the IPv4 pin")
	}
}

func TestNormalizePreservesExplicitValues(t *testing.T) {
	in := Config{
		BaseURL: "https://m.example.com", APIKey: "K",
		Timeout: 5 * time.Second, AppNamePrefix: "custom", HostHeader: "vhost.example",
	}
	c, err := in.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if c.Timeout != 5*time.Second || c.AppNamePrefix != "custom" || c.HostHeader != "vhost.example" {
		t.Errorf("Normalize overwrote explicit values: %+v", c)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv(EnvBaseURL, "https://mail.example.com")
	t.Setenv(EnvAPIKey, "AAAAAA-BBBBBB")
	t.Setenv(EnvAPIKeyFile, "")
	t.Setenv(EnvHostHeader, "mail.example.com")
	t.Setenv(EnvInsecureSkipVerify, "")

	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.APIKey != "AAAAAA-BBBBBB" {
		t.Errorf("APIKey did not load")
	}
	if c.BaseURL != "https://mail.example.com/api/v1" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c.HostHeader != "mail.example.com" {
		t.Errorf("HostHeader = %q", c.HostHeader)
	}
}

func TestLoadConfigFromKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.key")
	if err := os.WriteFile(path, []byte("FILE-KEY-123\n"), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}
	t.Setenv(EnvBaseURL, "https://mail.example.com")
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAPIKeyFile, path)
	t.Setenv(EnvHostHeader, "")
	t.Setenv(EnvInsecureSkipVerify, "")

	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// The trailing newline every editor adds must not become part of the key.
	if c.APIKey != "FILE-KEY-123" {
		t.Errorf("APIKey = %q, want the trimmed file contents", c.APIKey)
	}
}

func TestLoadConfigRefusesBothKeySources(t *testing.T) {
	t.Setenv(EnvBaseURL, "https://mail.example.com")
	t.Setenv(EnvAPIKey, "INLINE")
	t.Setenv(EnvAPIKeyFile, filepath.Join(t.TempDir(), "api.key"))
	t.Setenv(EnvHostHeader, "")
	t.Setenv(EnvInsecureSkipVerify, "")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig accepted both key sources")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error %q does not explain the fix", err)
	}
}

func TestLoadConfigRejectsBadBool(t *testing.T) {
	t.Setenv(EnvBaseURL, "https://mail.example.com")
	t.Setenv(EnvAPIKey, "K")
	t.Setenv(EnvAPIKeyFile, "")
	t.Setenv(EnvHostHeader, "")
	t.Setenv(EnvInsecureSkipVerify, "maybe")

	if _, err := LoadConfig(); err == nil {
		t.Fatal("LoadConfig accepted a non-boolean")
	}
}

func TestConfigStringRedactsTheKey(t *testing.T) {
	const key = "VERY-SECRET-KEY"
	c, err := Config{BaseURL: "https://m.example.com", APIKey: key}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	s := c.String()
	if strings.Contains(s, key) {
		t.Fatalf("String() leaks the API key: %s", s)
	}
	if !strings.Contains(s, "api_key=***") {
		t.Errorf("String() does not mark the key as redacted: %s", s)
	}
	// The non-secret parts must still be there, or the rendering has no value.
	if !strings.Contains(s, "https://m.example.com/api/v1") {
		t.Errorf("String() dropped the base URL: %s", s)
	}
}
