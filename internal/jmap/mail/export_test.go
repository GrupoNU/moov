package mail

// Test-only exports.
//
// The parity test lives in package mail_test (it compares this package's
// rendering against captured jmap-perl responses, which is a black-box
// concern), so it needs access to three internals. Exporting them through a
// _test.go file keeps them out of the production API surface entirely: this
// file is not compiled into the package a consumer imports.

// RenderMailboxForTest renders a Mailbox object with every property, through
// the production renderer.
func RenderMailboxForTest(row MailboxRow) map[string]any {
	return renderMailbox(row, nil)
}

// DefaultEmailProperties returns the RFC 8621 §4.6 default property list.
func DefaultEmailProperties() []string {
	out := make([]string, len(defaultEmailProperties))
	copy(out, defaultEmailProperties)
	return out
}

// TruncateForTest applies the production body-value truncation and reports the
// resulting value and whether it was cut.
func TruncateForTest(value string, maxOctets uint64) (string, bool) {
	bv := newBodyValue(value, maxOctets, false)
	return bv.Value, bv.IsTruncated
}
