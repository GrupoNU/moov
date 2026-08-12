package jmap

// The capability URIs Moov's phase-1 server implements.
//
// A capability URI names a specification the client opts into via the "using"
// property of the Request object (RFC 8620 §3.3). The server advertises the
// full set as keys of the Session object's "capabilities" property
// (RFC 8620 §2).
const (
	// CapCore is the JMAP core capability (RFC 8620 §2). Every server MUST
	// advertise it, and every request that calls Core/echo must include it in
	// "using".
	CapCore = "urn:ietf:params:jmap:core"

	// CapMail is the JMAP mail capability (RFC 8621 §1.3.1): Mailbox, Thread,
	// Email and SearchSnippet data types. Its value in the session
	// "capabilities" object is an empty object; the per-account limits live in
	// the Account's accountCapabilities (RFC 8621 §1.3.1).
	CapMail = "urn:ietf:params:jmap:mail"
)
