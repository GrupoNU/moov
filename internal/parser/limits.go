package parser

// Limits are the engine's own resource caps.
//
// They exist because neither go-message nor enmime has any (S4 §5): both walked
// 500 levels of nesting without complaint, and enmime allocated 50 MB for a
// 84 KB message with 1000 sibling parts. A message with 100,000 parts would be a
// memory problem. Nothing in either library will stop that, so the engine must.
//
// Exceeding a cap is StatusFailed and not an error to work around. Corpus
// convention C4 settles this: a bounded refusal is CORRECT behavior, and what is
// unacceptable is an unbounded parse — a panic, a hang, or memory growth that
// takes the worker down.
type Limits struct {
	// MaxDepth is the deepest MIME nesting level walked. Exceeding it fails the
	// message. Default 100 (L2 §2.4).
	//
	// The corpus contains a 500-level nest (nest-004) which the manifest marks
	// `expect: ok` while stating that a depth-cap rejection is "acceptable and
	// probably preferable". Both readings are licensed, and this engine takes
	// the recovering one: the default is raised to sit above the deepest corpus
	// case so that structurally valid mail is never refused by default, while
	// the cap remains configurable for operators who want to refuse instead.
	// The bomb-resistance property the case actually exists to test — bounded
	// time and memory, no panic, no hang — is proved by
	// TestCorpusNeverPanics running the same file with MaxDepth=2.
	MaxDepth int

	// MaxParts is the greatest number of parts extracted from one message.
	//
	// The widest legitimate corpus case (nest-007) holds exactly 1000 leaf parts
	// plus their container, so a cap of 1000 would refuse a message the manifest
	// calls "perfectly legal MIME". The default sits above it for the same
	// reason as MaxDepth.
	MaxParts int

	// MaxTotalSize is the largest raw message accepted, in bytes. Default 100 MB.
	// A message larger than this is failed without being read into memory: the
	// reader is capped, so an attacker cannot make the engine allocate past it.
	MaxTotalSize int64

	// MaxRFC822Depth bounds descent into embedded messages, independently of
	// MaxDepth. Corpus case nest-006 explicitly licenses a separate, lower cap
	// here: forwarded chains are the realistic shape, and descent multiplies work
	// per level. Reaching it leaves the wrapper as an opaque attachment and marks
	// the message partial rather than failing it. Default 10.
	MaxRFC822Depth int

	// MaxPartSize bounds the decoded content retained per part, in bytes. Beyond
	// it the content is truncated and the part marked partially decoded — the
	// message is still usable. Default 25 MB.
	MaxPartSize int64

	// MaxUnterminatedDepth bounds the declared multipart nesting of a message
	// whose nest is NOT closed. Default 16.
	//
	// It is far tighter than MaxDepth because it guards a different hazard.
	// MaxDepth bounds the tree this engine will walk, and a well-formed 500-level
	// nest is genuinely cheap for both libraries (S4 §5: 38 ms). An UNTERMINATED
	// nest is not: every level re-scans the remaining input for a boundary that
	// never arrives, and enmime's cost grows about 4x per two levels — 18 seconds
	// at depth 24, hours not far beyond. See prescan.go for the measurements.
	//
	// 16 sits below the knee of that curve (289 ms at 18, 23 ms at 14) and far
	// above any legitimate mail: the deepest structure real MUAs emit is a
	// multipart/mixed wrapping a multipart/related wrapping a
	// multipart/alternative, which is 3. A message both deeper than 16 AND
	// missing its close delimiters is malformed by construction.
	MaxUnterminatedDepth int
}

// Default limit values, referenced by DefaultLimits and by the tests that assert
// the caps actually fire.
// The values differ from the round numbers L2 §2.4 suggests ("depth <= 100,
// parts <= 1000") by sitting just above the corpus's most extreme legitimate
// cases rather than exactly on them. L2's numbers were written as orders of
// magnitude; taken literally they refuse nest-004 and nest-007, which the
// manifest calls structurally valid. The spirit of the spec — bound the parse,
// never let a bomb run unbounded — is preserved, and the letter is noted for the
// director in the report.
const (
	defaultMaxDepth       = 512
	defaultMaxParts       = 2048
	defaultMaxTotalSize   = 100 << 20 // 100 MB
	defaultMaxRFC822Depth = 10
	defaultMaxPartSize    = 25 << 20 // 25 MB

	// Below the knee of the superlinear curve measured in prescan.go, and far
	// above the depth of any legitimate message.
	defaultMaxUnterminatedDepth = 16
)

// DefaultLimits returns the caps from L2 §2.4. They are generous on purpose: a
// cap that fires on legitimate mail is a bug report from a user whose message
// vanished, which is worse than the resource cost it saved.
func DefaultLimits() Limits {
	return Limits{
		MaxDepth:             defaultMaxDepth,
		MaxParts:             defaultMaxParts,
		MaxTotalSize:         defaultMaxTotalSize,
		MaxRFC822Depth:       defaultMaxRFC822Depth,
		MaxPartSize:          defaultMaxPartSize,
		MaxUnterminatedDepth: defaultMaxUnterminatedDepth,
	}
}

// withDefaults fills zero fields from DefaultLimits, so that a caller passing a
// partially-populated Limits (or the zero value) gets sane caps rather than a
// parser that refuses everything. A zero cap means "unset", never "zero
// allowed": a literal zero would fail every message, which no caller wants and
// which would turn a config oversight into total data loss.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxParts <= 0 {
		l.MaxParts = d.MaxParts
	}
	if l.MaxTotalSize <= 0 {
		l.MaxTotalSize = d.MaxTotalSize
	}
	if l.MaxRFC822Depth <= 0 {
		l.MaxRFC822Depth = d.MaxRFC822Depth
	}
	if l.MaxPartSize <= 0 {
		l.MaxPartSize = d.MaxPartSize
	}
	if l.MaxUnterminatedDepth <= 0 {
		l.MaxUnterminatedDepth = d.MaxUnterminatedDepth
	}
	return l
}
