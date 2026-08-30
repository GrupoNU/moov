// Package sieve is the only package in Moov that speaks ManageSieve
// (RFC 5804).
//
// # Architecture rule
//
// The same confinement discipline internal/imap applies to go-imap applies
// here to the ManageSieve wire protocol: no raw protocol detail — commands,
// response codes, literals, capability lines — may appear outside this
// package. Everything else in Moov talks to Dovecot's script storage through
// the Client interface, expressed in Moov's own types. There is no external
// library to confine (the protocol is small enough that this package IS the
// implementation), so the rule here is about the wire format itself.
//
// # What this package covers (L3 epic E6, block C0)
//
//	Connect       STARTTLS on dovecot:4190, AUTHENTICATE PLAIN with the
//	              account's app password (scope sieve, ADR §4), capabilities
//	              re-read after the TLS upgrade (RFC 5804 §2.2)
//	ListScripts   LISTSCRIPTS with the ACTIVE marker
//	GetScript     GETSCRIPT
//	PutScript     PUTSCRIPT, surfacing the WARNINGS response code
//	CheckScript   CHECKSCRIPT (validate without storing)
//	SetActive     SETACTIVE, including the empty-string deactivation
//	DeleteScript  DELETESCRIPT, distinguishing ACTIVE and NONEXISTENT
//	RenameScript  RENAMESCRIPT (core in RFC 5804: VERSION "1.0" implies it)
//
// The capability list matters beyond diagnostics: the SIEVE capability's
// extension list governs what the script generator (generate.go) may emit,
// and a rule needing an unadvertised extension is refused at validate time
// rather than pushed and broken.
//
// # TLS posture
//
// STARTTLS on the cleartext port, certificate verification always on — the
// exact posture internal/imap takes for the same deployment (the connection
// never leaves the Docker network, the certificate belongs to the public mail
// hostname, so TLSServerName exists here for the same S1 H2 reason). A server
// that does not offer STARTTLS is refused: our Dovecot advertises an EMPTY
// SASL list before the TLS upgrade (verified on the wire against Mailcow's
// Dovecot 2.3.21.1), so there is no authenticated cleartext mode to fall back
// to, and this package deliberately does not implement one.
//
// # Concurrency
//
// A Client is one command stream and is NOT safe for concurrent use — the
// same contract as imap.Client, for the same reason. Callers that need
// serialization put it around the Client, not inside it. ManageSieve traffic
// is settings-frequency (a user editing filters), so the production shape is
// dial-per-operation rather than pooling; see internal/jmap/mail's adapter.
package sieve
