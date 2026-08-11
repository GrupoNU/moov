package sync

import (
	"math"

	"github.com/GrupoNU/moov/internal/imap"
)

// Numeric conversions between the IMAP wire types and the store's columns.
//
// They exist as named functions, in one file, because every one of them crosses
// a signedness or width boundary where a silent wrap would corrupt a message's
// identity rather than fail. Written inline as casts they are six unremarkable
// expressions; written here they are six statements of what the bound IS and
// what happens at it.
//
// The widths involved:
//
//	IMAP UID      uint32   -> store uid          bigint (int64)  — always fits
//	IMAP MODSEQ   uint64   -> store modseq_seen  bigint (int64)  — clamped
//	UIDVALIDITY   uint32   -> store uidvalidity  bigint (int64)  — always fits
//	store column  int64    -> IMAP UIDVALIDITY   uint32          — clamped
//	config int             -> IMAP UID           uint32          — clamped

// modSeqToDB converts a CONDSTORE modification sequence to the bigint column.
//
// MODSEQ is a uint64 on the wire and the column is signed, so the top bit is
// not representable. In practice a modseq is a per-mailbox counter that
// increments once per change: reaching 2^63 would take longer than the age of
// the universe at any plausible rate. The clamp is here so that a server
// reporting a nonsense value produces a saturated cursor — which resyncs
// harmlessly — rather than a negative one, which would make every subsequent
// CHANGEDSINCE query silently match everything.
func modSeqToDB(m imap.ModSeq) int64 {
	if m > math.MaxInt64 {
		return math.MaxInt64
	}
	// #nosec G115 -- the guard above establishes m <= MaxInt64, so the
	// conversion is exact. gosec does not track the comparison; the same
	// pattern and the same justification appear in internal/store's Flags.toDB.
	return int64(m)
}

// uidValidityToDB widens a UIDVALIDITY for storage. A uint32 always fits in an
// int64, so this cannot lose information; it is named for symmetry with its
// inverse, which can.
func uidValidityToDB(v uint32) int64 { return int64(v) }

// uidValidityFromDB narrows a stored UIDVALIDITY back to the wire type.
//
// A value outside the uint32 range cannot have come from a server and means the
// row is corrupt. Returning 0 is the safe answer: SelectQResync treats 0 as
// "never synced" and selects the mailbox plainly, so the mailbox resyncs from
// scratch instead of being compared against a fabricated validity.
func uidValidityFromDB(v int64) uint32 {
	if v < 0 || v > math.MaxUint32 {
		return 0
	}
	return uint32(v)
}

// uidNextToDB widens a UID for storage. Like uidValidityToDB it cannot lose
// information.
func uidNextToDB(u imap.UID) int64 { return int64(u) }

// windowSize converts a configured window size to the UID type.
//
// The window comes from configuration, so it is an int that could in principle
// be larger than the UID space or negative. Clamping to the UID space keeps the
// descending scan's arithmetic total: a window wider than every UID that can
// exist simply covers the whole mailbox in one pass, which is correct.
func windowSize(n int) imap.UID {
	switch {
	case n <= 0:
		return imap.UID(DefaultFetchWindow)
	case int64(n) > int64(math.MaxUint32):
		return imap.UID(math.MaxUint32)
	default:
		// #nosec G115 -- the two cases above establish 0 < n <= MaxUint32, so
		// the conversion is exact. gosec does not track the switch.
		return imap.UID(n)
	}
}
