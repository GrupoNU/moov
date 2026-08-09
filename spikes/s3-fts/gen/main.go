// Command gen produces a deterministic synthetic mail corpus for Spike S3 in
// PostgreSQL COPY text format on stdout.
//
//	gen -n 5000000 -seed 20260808 | psql -c "COPY messages (...) FROM STDIN"
//
// Design goals:
//   - Deterministic: same -seed and -n always produce byte-identical output, so
//     the benchmark is reproducible and the planted needles are stable.
//   - Realistic token statistics: Zipfian vocabulary over two wordlists
//     (Spanish/English ~60/40), Zipfian correspondent pool, log-normal body
//     lengths, dates denser toward the present.
//   - Streaming: writes as it generates, never materialises the corpus in RAM.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"time"
)

const (
	numContacts = 50000 // correspondent pool size
	numAccounts = 89    // mirrors the real Crash stress case
)

// ---------------------------------------------------------------------------
// Planted needles — correctness checks for the benchmark queries.
// ---------------------------------------------------------------------------

const (
	// needleToken appears in exactly needleTokenCount messages, corpus-wide.
	needleToken      = "zanzibarita"
	needleTokenCount = 37

	// needleUnique appears in exactly one message, corpus-wide.
	needleUnique = "INV-2024-0857"

	// needlePhrase appears in exactly needlePhraseCount messages, as a
	// contiguous phrase, corpus-wide.
	needlePhrase      = "quetzal ferroviario nocturno"
	needlePhraseCount = 5
)

// needleAccounts spreads the needleToken hits across a few accounts so the
// per-account correctness checks are meaningful: account 1 (the 1M-message
// power user), account 2, and a mid-size account.
var needleAccounts = []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 10 in account 1
	2, 2, 2, 2, 2, 2, 2, 2, // 8 in account 2
	3, 3, 3, 3, 3, // 5 in account 3
	17, 17, 17, 17, // 4 in account 17
	42, 42, 42, // 3 in account 42
	55, 55, 60, 60, 71, 78, 83} // 7 scattered

// ---------------------------------------------------------------------------
// Zipf sampling
// ---------------------------------------------------------------------------

// zipf holds a precomputed cumulative distribution for a Zipf-like law over n
// items with exponent s. Sampling is a binary search over the CDF: O(log n),
// and unlike rand.Zipf it lets us reuse one table across goroutine-free runs.
type zipf struct {
	cdf []float64
}

func newZipf(n int, s float64) *zipf {
	cdf := make([]float64, n)
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += 1.0 / math.Pow(float64(i+1), s)
		cdf[i] = sum
	}
	for i := range cdf {
		cdf[i] /= sum
	}
	return &zipf{cdf: cdf}
}

func (z *zipf) sample(r *rand.Rand) int {
	u := r.Float64()
	lo, hi := 0, len(z.cdf)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if z.cdf[mid] < u {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// ---------------------------------------------------------------------------
// Account / mailbox model
// ---------------------------------------------------------------------------

type account struct {
	id       int
	count    int   // messages in this account
	mailboxes int  // number of mailboxes
	mboxCDF  []float64
}

// buildAccounts distributes total messages: account 1 = 1,000,000;
// account 2 = 500,000; account 3 = 250,000; accounts 4..89 share the rest
// log-normally (a few mid-size mailboxes, a long tail of small ones).
func buildAccounts(total int, r *rand.Rand) []account {
	accs := make([]account, 0, numAccounts)
	head := []int{1000000, 500000, 250000}
	assigned := 0
	for i, c := range head {
		if c > total-assigned {
			c = total - assigned
		}
		accs = append(accs, account{id: i + 1, count: c})
		assigned += c
	}

	rest := total - assigned
	n := numAccounts - len(head)
	weights := make([]float64, n)
	wsum := 0.0
	for i := range weights {
		// log-normal weights: mu=0, sigma=1.1 gives a realistic spread where
		// the largest tail account is ~50x the smallest.
		weights[i] = math.Exp(r.NormFloat64() * 1.1)
		wsum += weights[i]
	}
	allocated := 0
	for i := 0; i < n; i++ {
		c := int(float64(rest) * weights[i] / wsum)
		if i == n-1 {
			c = rest - allocated // last account absorbs rounding
		}
		if c < 1 {
			c = 1
		}
		allocated += c
		accs = append(accs, account{id: len(head) + i + 1, count: c})
	}

	// Mailboxes: 10-15 per account. INBOX (id 1) dominates, Sent (id 2) is
	// second, the rest share the remainder — matching real folder use.
	for i := range accs {
		nb := 10 + r.Intn(6)
		accs[i].mailboxes = nb
		w := make([]float64, nb)
		w[0] = 55.0 // INBOX
		if nb > 1 {
			w[1] = 18.0 // Sent
		}
		for j := 2; j < nb; j++ {
			w[j] = 1.0 + r.Float64()*6.0
		}
		sum := 0.0
		for _, x := range w {
			sum += x
		}
		cdf := make([]float64, nb)
		acc := 0.0
		for j, x := range w {
			acc += x / sum
			cdf[j] = acc
		}
		accs[i].mboxCDF = cdf
	}
	return accs
}

func sampleCDF(cdf []float64, r *rand.Rand) int {
	u := r.Float64()
	for i, c := range cdf {
		if u <= c {
			return i
		}
	}
	return len(cdf) - 1
}

// ---------------------------------------------------------------------------
// Contacts
// ---------------------------------------------------------------------------

type contactPool struct {
	addrs []string
	z     *zipf
}

func buildContacts(r *rand.Rand) *contactPool {
	addrs := make([]string, numContacts)
	seen := make(map[string]bool, numContacts)
	for i := 0; i < numContacts; i++ {
		var a string
		for {
			fn := strings.ToLower(firstNames[r.Intn(len(firstNames))])
			ln := strings.ToLower(lastNames[r.Intn(len(lastNames))])
			dom := strings.ToLower(companyStems[r.Intn(len(companyStems))])
			tld := tlds[r.Intn(len(tlds))]
			switch r.Intn(4) {
			case 0:
				a = fmt.Sprintf("%s.%s@%s.%s", fn, ln, dom, tld)
			case 1:
				a = fmt.Sprintf("%s%s@%s.%s", fn[:1], ln, dom, tld)
			case 2:
				a = fmt.Sprintf("%s@%s.%s", fn, dom, tld)
			default:
				a = fmt.Sprintf("%s.%s%d@%s.%s", fn, ln, r.Intn(90)+10, dom, tld)
			}
			if !seen[a] {
				seen[a] = true
				break
			}
		}
		addrs[i] = a
	}
	// s=0.9: a handful of correspondents account for a large share of traffic,
	// which is what real mailboxes look like.
	return &contactPool{addrs: addrs, z: newZipf(numContacts, 0.9)}
}

func (c *contactPool) pick(r *rand.Rand) string { return c.addrs[c.z.sample(r)] }

// ---------------------------------------------------------------------------
// Text generation
// ---------------------------------------------------------------------------

type vocab struct {
	es, en   []string
	zes, zen *zipf
}

func buildVocab() *vocab {
	return &vocab{
		es:  spanishWords,
		en:  englishWords,
		zes: newZipf(len(spanishWords), 1.05),
		zen: newZipf(len(englishWords), 1.05),
	}
}

// word draws from the Spanish list with probability esProb, else English.
func (v *vocab) word(r *rand.Rand, esProb float64) string {
	if r.Float64() < esProb {
		return v.es[v.zes.sample(r)]
	}
	return v.en[v.zen.sample(r)]
}

func properNoun(r *rand.Rand) string {
	switch r.Intn(3) {
	case 0:
		return firstNames[r.Intn(len(firstNames))]
	case 1:
		return lastNames[r.Intn(len(lastNames))]
	default:
		return companyStems[r.Intn(len(companyStems))] + " " + companySuffixes[r.Intn(len(companySuffixes))]
	}
}

// codeToken emits the structured tokens that pepper real business mail:
// invoice numbers, order codes, URLs, phone numbers.
func codeToken(r *rand.Rand) string {
	switch r.Intn(4) {
	case 0:
		// Year range 2016-2023 deliberately EXCLUDES 2024 so the planted
		// unique needle INV-2024-0857 cannot be produced by chance.
		return fmt.Sprintf("INV-%d-%04d", 2016+r.Intn(8), r.Intn(10000))
	case 1:
		return fmt.Sprintf("ORD%07d", r.Intn(10000000))
	case 2:
		return fmt.Sprintf("https://%s.%s/%s/%d",
			strings.ToLower(companyStems[r.Intn(len(companyStems))]),
			tlds[r.Intn(len(tlds))],
			strings.ToLower(englishWords[r.Intn(60)]),
			r.Intn(100000))
	default:
		return fmt.Sprintf("+54-9-11-%04d-%04d", r.Intn(10000), r.Intn(10000))
	}
}

// logNormalInt draws a positive integer from a log-normal distribution with the
// given median and sigma, clamped to [min,max].
func logNormalInt(r *rand.Rand, median float64, sigma float64, min, max int) int {
	v := int(median * math.Exp(r.NormFloat64()*sigma))
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (v *vocab) subject(r *rand.Rand, esProb float64) string {
	n := 3 + r.Intn(8) // 3-10 words
	parts := make([]string, 0, n+2)
	if r.Float64() < 0.18 {
		parts = append(parts, "Re:")
	} else if r.Float64() < 0.06 {
		parts = append(parts, "Fwd:")
	}
	for i := 0; i < n; i++ {
		switch {
		case r.Float64() < 0.12:
			parts = append(parts, properNoun(r))
		case r.Float64() < 0.06:
			parts = append(parts, codeToken(r))
		default:
			parts = append(parts, v.word(r, esProb))
		}
	}
	return strings.Join(parts, " ")
}

// body builds a message body of log-normal length (median ~120 words,
// p99 ~2000) as sentences of 8-18 words.
func (v *vocab) body(r *rand.Rand, esProb float64, sb *strings.Builder) {
	sb.Reset()
	// sigma=1.22 with median 120 puts p99 near 2000 words.
	total := logNormalInt(r, 120, 1.22, 5, 6000)
	written := 0
	for written < total {
		slen := 8 + r.Intn(11)
		if slen > total-written {
			slen = total - written
		}
		for i := 0; i < slen; i++ {
			if sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			switch {
			case r.Float64() < 0.05:
				sb.WriteString(properNoun(r))
			case r.Float64() < 0.03:
				sb.WriteString(codeToken(r))
			default:
				sb.WriteString(v.word(r, esProb))
			}
		}
		sb.WriteString(". ")
		written += slen
	}
}

// ---------------------------------------------------------------------------
// COPY text-format escaping
// ---------------------------------------------------------------------------

// copyEscape escapes the characters that PostgreSQL COPY text format treats
// specially. Our generator never emits newlines or backslashes, but escaping
// defensively keeps the loader honest if the vocabulary ever changes.
func copyEscape(s string) string {
	if !strings.ContainsAny(s, "\\\n\r\t") {
		return s
	}
	rep := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return rep.Replace(s)
}

// ---------------------------------------------------------------------------
// Dates
// ---------------------------------------------------------------------------

var corpusEnd = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

// sampleDate spreads messages over 10 years, denser toward the present:
// age in days ~ Exponential-ish via inverse power, so roughly 40% of mail lands
// in the last 12 months, matching how mailboxes actually accumulate.
func sampleDate(r *rand.Rand) time.Time {
	const spanDays = 3650.0
	u := r.Float64()
	// x^2.2 biases the draw toward 0 (recent).
	ageDays := math.Pow(u, 2.2) * spanDays
	d := corpusEnd.Add(-time.Duration(ageDays * 24 * float64(time.Hour)))
	// business-hours skew
	d = d.Add(time.Duration(r.Intn(86400)) * time.Second)
	return d
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	var (
		n     = flag.Int("n", 5000000, "number of messages to generate")
		seed  = flag.Int64("seed", 20260808, "RNG seed (determinism)")
		esPro = flag.Float64("es", 0.60, "probability a word is drawn from the Spanish list")
	)
	flag.Parse()

	r := rand.New(rand.NewSource(*seed))
	accs := buildAccounts(*n, r)
	contacts := buildContacts(r)
	v := buildVocab()

	// Decide, up front, which global message indices carry the planted needles.
	// Using a separate RNG stream keyed off the seed keeps needle placement
	// stable regardless of how many random draws the body generator makes.
	nr := rand.New(rand.NewSource(*seed ^ 0x5eed))
	needleTok := map[int]bool{}   // message index -> carries needleToken
	needlePhr := map[int]bool{}   // message index -> carries needlePhrase
	needleUniqIdx := -1

	// Build the per-account index ranges so needles can target accounts.
	start := make([]int, len(accs))
	off := 0
	for i, a := range accs {
		start[i] = off
		off += a.count
	}
	idxOf := func(accountID int) int {
		for i, a := range accs {
			if a.id == accountID && a.count > 0 {
				return start[i] + nr.Intn(a.count)
			}
		}
		return nr.Intn(*n)
	}
	// All needle placement uses bounded retries with a corpus-wide fallback:
	// with a small -n the tail accounts hold only one message each, so an
	// unbounded "retry until distinct within this account" loop would spin
	// forever. The needle COUNTS are what the correctness checks assert, so
	// falling back to a different account is acceptable; at the full 5M size
	// the fallback never triggers.
	const maxTries = 1000
	placed := 0
	for _, acctID := range needleAccounts {
		ok := false
		for t := 0; t < maxTries; t++ {
			idx := idxOf(acctID)
			if !needleTok[idx] {
				needleTok[idx] = true
				ok = true
				break
			}
		}
		if !ok {
			for t := 0; t < maxTries*100; t++ {
				idx := nr.Intn(*n)
				if !needleTok[idx] {
					needleTok[idx] = true
					ok = true
					break
				}
			}
		}
		if ok {
			placed++
		}
	}
	if placed != needleTokenCount {
		fmt.Fprintf(os.Stderr, "WARNING: placed %d needleToken messages, wanted %d (corpus too small?)\n",
			placed, needleTokenCount)
	}
	for t := 0; len(needlePhr) < needlePhraseCount && t < maxTries*100; t++ {
		idx := nr.Intn(*n)
		if !needleTok[idx] && !needlePhr[idx] {
			needlePhr[idx] = true
		}
	}
	for t := 0; t < maxTries*100; t++ {
		idx := nr.Intn(*n)
		if !needleTok[idx] && !needlePhr[idx] {
			needleUniqIdx = idx
			break
		}
	}

	w := bufio.NewWriterSize(os.Stdout, 1<<22) // 4 MiB
	defer w.Flush()

	var sb strings.Builder
	sb.Grow(64 << 10)

	global := 0
	for ai := range accs {
		a := &accs[ai]
		uid := int64(1)
		for k := 0; k < a.count; k++ {
			mbox := sampleCDF(a.mboxCDF, r) + 1
			date := sampleDate(r)

			// flags bitmask: bit 0 = \Seen. ~30% unread => bit 0 clear.
			flags := 0
			if r.Float64() >= 0.30 {
				flags |= 1
			}
			if r.Float64() < 0.08 {
				flags |= 2 // \Flagged
			}
			if r.Float64() < 0.04 {
				flags |= 4 // \Answered
			}

			from := contacts.pick(r)
			nTo := 1
			if r.Float64() < 0.25 {
				nTo = 2 + r.Intn(3)
			}
			tos := make([]string, nTo)
			for i := range tos {
				tos[i] = contacts.pick(r)
			}
			to := strings.Join(tos, ", ")

			subject := v.subject(r, *esPro)
			v.body(r, *esPro, &sb)
			body := sb.String()

			// Plant needles.
			if needleTok[global] {
				body = body + " " + needleToken + "."
			}
			if needlePhr[global] {
				body = body + " " + needlePhrase + "."
			}
			if global == needleUniqIdx {
				body = body + " " + needleUnique + "."
			}

			fmt.Fprintf(w, "%d\t%d\t%d\t%s\t%d\t%s\t%s\t%s\t%s\n",
				a.id, mbox, uid,
				date.UTC().Format("2006-01-02 15:04:05-07"),
				flags,
				copyEscape(from), copyEscape(to),
				copyEscape(subject), copyEscape(body))

			uid++
			global++
		}
	}

	// Emit the needle manifest on stderr so the bench driver can assert
	// exact counts without re-deriving them.
	fmt.Fprintf(os.Stderr, "generated %d messages across %d accounts\n", global, len(accs))
	fmt.Fprintf(os.Stderr, "needle_token=%s count=%d\n", needleToken, len(needleTok))
	fmt.Fprintf(os.Stderr, "needle_phrase=%q count=%d\n", needlePhrase, len(needlePhr))
	fmt.Fprintf(os.Stderr, "needle_unique=%s count=1\n", needleUnique)
}
