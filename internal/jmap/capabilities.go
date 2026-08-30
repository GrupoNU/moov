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

	// CapSubmission is the JMAP submission capability (RFC 8621 §1.3.2):
	// Identity and EmailSubmission data types (W3). Like the mail capability,
	// its session value is an empty object and the per-account values
	// (maxDelayedSend, submissionExtensions) live in accountCapabilities.
	CapSubmission = "urn:ietf:params:jmap:submission"

	// CapPrefs is Moov's VENDOR capability for per-account preferences (L3
	// epic E0): the Prefs singleton and its /get and /set methods.
	//
	// # Why a vendor capability rather than an extension of the mail one
	//
	// RFC 8620 §2 is explicit that this is the mechanism: "Servers MAY add
	// additional properties to the [capabilities] object [...] Vendors MUST
	// use a URI they control as the property name." §1.8 completes the
	// contract from the client's side: "The client MUST opt in to use an
	// extension by passing the appropriate capability identifier in the
	// 'using' array [...] The server MUST only follow the specifications that
	// are opted into and behave as though it does not implement anything else
	// when processing a request."
	//
	// Those two sentences together are exactly the property epic E0 needs and
	// an extra key on urn:ietf:params:jmap:mail could not give: a client that
	// has never heard of Moov's preferences — Bulwark, or any other JMAP
	// client — never puts this URI in "using", never sees Prefs/get exist, and
	// is completely unaffected. Bolting preference properties onto the
	// standard mail capability would instead hand every conforming client
	// object properties RFC 8621 does not define, in a namespace it is
	// entitled to assume it understands.
	//
	// # The URI
	//
	// An https URI under a domain Moov controls, per §2's "a URI they control"
	// requirement. It is an identifier, not an endpoint: nothing dereferences
	// it, and it deliberately does not use the urn:ietf:params:jmap: prefix,
	// which is an IANA registry (RFC 8620 §8.4) reserved for standards-track
	// specifications and which a vendor extension must not squat.
	CapPrefs = "https://moov.email/ns/prefs"

	// CapTriage is Moov's VENDOR capability for the triage verbs RFC 8621 has
	// no vocabulary for: snooze and mute (L3 epic E4, canon §2.2).
	//
	// # Why these need a capability of their own
	//
	// Both are CORE Gmail behaviors and neither exists in JMAP. RFC 8621 §4
	// has keywords, mailboxes and a mutable `keywords` map; it has no notion
	// of "come back later" and no per-Thread state at all (§3 gives a Thread
	// exactly two properties, id and emailIds, both server-set). So there is
	// nothing to extend — the objects have to be new, and RFC 8620 §2's vendor
	// mechanism is where new objects go.
	//
	// It is a SEPARATE URI from CapPrefs rather than more methods under it,
	// because §1.8's opt-in is per capability and the two answer different
	// questions: a client may well want the settings surface without the
	// triage verbs (a settings-only admin UI) or the triage verbs without the
	// settings (a keyboard-driven client with its own preferences). Fusing
	// them would make "I understand snooze" and "I understand density" the
	// same statement.
	//
	// A client that never names this URI never sees Snooze/set or Mute/set
	// exist, and — the property that matters for the Bulwark oracle — is
	// completely unaffected by their presence.
	CapTriage = "https://moov.email/ns/triage"

	// CapSieve is the JMAP Sieve capability (RFC 9661 §1.2.1): the
	// SieveScript data type and its /get, /set, /validate and /query methods
	// (L3 epic E6). This is the standard surface that unlocks Bulwark's
	// Filters tab. Its account capability object carries the §1.2.1 limits
	// and the server's live SIEVE extension list.
	CapSieve = "urn:ietf:params:jmap:sieve"

	// CapVacation is the JMAP vacation-response capability (RFC 8621 §1.3.3,
	// §8): the VacationResponse singleton. Its session and account values
	// are empty objects per §1.3.3.
	CapVacation = "urn:ietf:params:jmap:vacationresponse"

	// CapQuota is the JMAP quota capability (RFC 9425 §2.1): the Quota data
	// type. §2.1: "The value of this property is an empty object in both the
	// JMAP session capabilities property and an account's
	// accountCapabilities property."
	CapQuota = "urn:ietf:params:jmap:quota"

	// CapFilters is Moov's VENDOR capability for the server-side rule model
	// (L3 epic E6, GC-4): FilterRule and ForwardingAddress objects.
	//
	// # Why a vendor surface EXISTS next to urn:ietf:params:jmap:sieve
	//
	// RFC 9661 moves raw scripts; it has no notion of a rule, a blocked
	// sender, or a verified forwarding address. Bulwark solves that
	// client-side (an ~800-line Sieve tokenizer in the browser). Moov's UI
	// gets the rule model SERVER-side instead — the parse/generate round
	// trip, the origin partitioning and the verified-forward enforcement
	// live in internal/sieve where they are pinned by tests — so the UI
	// renders "Filtros", "Bloqueados" and "Reenvío" from typed objects. Same
	// vendor-URI reasoning as CapPrefs; a client that never opts in is
	// unaffected.
	CapFilters = "https://moov.email/ns/filters"
)
