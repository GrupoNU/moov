package main

import (
	"fmt"
	"os"
)

// Config holds the IMAP connection parameters for the spike.
// Everything comes from the environment: no credential ever touches the repo.
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
}

func loadConfig() (*Config, error) {
	c := &Config{
		Host:     os.Getenv("IMAP_HOST"),
		Port:     os.Getenv("IMAP_PORT"),
		User:     os.Getenv("IMAP_USER"),
		Password: os.Getenv("IMAP_PASSWORD"),
	}
	if c.Host == "" || c.Port == "" || c.User == "" || c.Password == "" {
		return nil, fmt.Errorf("IMAP_HOST, IMAP_PORT, IMAP_USER and IMAP_PASSWORD must all be set")
	}
	return c, nil
}

func (c *Config) Addr() string { return c.Host + ":" + c.Port }

// redact replaces the password with a placeholder so protocol transcripts are
// safe to publish. Must be applied to every line we print.
func (c *Config) redact(s string) string {
	if c.Password == "" {
		return s
	}
	return replaceAll(s, c.Password, "<REDACTED>")
}

func replaceAll(s, old, new string) string {
	if old == "" {
		return s
	}
	out := ""
	for {
		i := indexOf(s, old)
		if i < 0 {
			return out + s
		}
		out += s[:i] + new
		s = s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	if len(sub) > len(s) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
