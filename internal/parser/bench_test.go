package parser

import (
	"os"
	"path/filepath"
	"testing"
)

// Benchmarks are informational, not gates. They exist to make a performance
// regression visible in a diff rather than in production: the sync engine
// budgets parse workers per CPU core (L2 §2.5, from S3 H6's measurement that
// to_tsvector is CPU-bound at ~2,063 rows/s), so parse cost per message is a
// number the capacity plan depends on.

// loadCorpusFiles reads every corpus case once, so the benchmark measures
// parsing rather than disk.
func loadCorpusFiles(tb testing.TB) [][]byte {
	tb.Helper()
	var out [][]byte
	err := filepath.WalkDir(corpusDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".eml" {
			return nil //nolint:nilerr // skip unreadable entries rather than failing
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // fixed test-data path
		if readErr == nil {
			out = append(out, data)
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("walking the corpus: %v", err)
	}
	if len(out) == 0 {
		tb.Skip("corpus not available")
	}
	return out
}

// BenchmarkParseCorpus parses the whole corpus once per iteration. The corpus is
// deliberately pathological, so this is a worst-case figure rather than a
// representative one — ordinary mail is the go-message fast path.
func BenchmarkParseCorpus(b *testing.B) {
	files := loadCorpusFiles(b)

	var total int64
	for _, f := range files {
		total += int64(len(f))
	}

	b.ReportAllocs()
	b.SetBytes(total)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		for _, raw := range files {
			_ = ParseBytes(raw, DefaultLimits())
		}
	}
}

// BenchmarkParseTypicalMessage measures the common path: a small
// multipart/alternative, which is what the overwhelming majority of real mail
// looks like and therefore what the per-core worker budget is actually built on.
func BenchmarkParseTypicalMessage(b *testing.B) {
	raw := []byte("From: Ada Lovelace <ada@example.com>\r\n" +
		"To: Grace Hopper <grace@example.org>\r\n" +
		"Subject: =?UTF-8?Q?Presupuesto_acci=C3=B3n?=\r\n" +
		"Date: Mon, 06 Jan 2025 10:00:00 +0000\r\n" +
		"Message-ID: <typical@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		"Hola, adjunto el presupuesto que discutimos ayer por la tarde.\r\n" +
		"--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>Hola, adjunto el presupuesto que discutimos ayer por la tarde.</p>\r\n" +
		"--b--\r\n")

	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = ParseBytes(raw, DefaultLimits())
	}
}

// BenchmarkParseByLayer separates the cascade's three layers, so a regression can
// be attributed to the layer that caused it.
func BenchmarkParseByLayer(b *testing.B) {
	cases := map[string]string{
		"go-message": "07-structural/014-alternative-inverted-order.eml",
		"enmime":     "09-real-world/009-mbox-from-line-leak.eml",
		"salvage":    "03-headers/009-leading-continuation-line.eml",
	}

	for name, file := range cases {
		raw, err := os.ReadFile(filepath.Join(corpusDir(), filepath.FromSlash(file))) //nolint:gosec // fixed test-data path
		if err != nil {
			b.Skipf("corpus not available: %v", err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ParseBytes(raw, DefaultLimits())
			}
		})
	}
}
