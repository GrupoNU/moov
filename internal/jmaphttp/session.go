package jmaphttp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/version"
)

// The JMAP Session resource (RFC 8620 §2), served DIRECTLY at
// /.well-known/jmap — no redirect. Spike S1 H7 found the redirect variant
// (well-known → /session) confuses real clients and complicates fronting
// proxies; §2.2's "following any redirects" permits a redirect but nothing
// requires one, and serving directly is the strictly simpler conforming
// choice.

// handleSession serves the authenticated Session object.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromContext(r.Context())
	if !ok {
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}
	// §2: "it is RECOMMENDED to disable HTTP caching altogether" for the
	// session — clients refetch only on a sessionState change.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	writeJSON(w, http.StatusOK, s.sessionObject(s.baseURL(r), id))
}

// sessionObject builds the RFC 8620 §2 Session object for one authenticated
// account.
//
// Truthfulness rules (regla J1: never advertise what is not enforced):
//
//   - The capability limits come from the SAME jmap.Limits the engine and
//     transport enforce.
//   - accounts contains exactly the caller's own account. Basic auth
//     authenticates one mailbox owner; there are no shared or delegated
//     accounts in phase 1, so isPersonal is true by construction.
//   - isReadOnly is false since W1: Email/set is registered and really
//     mutates the mailbox (flags, moves, destroy per W-A2). §2 defines the
//     flag as "true if the entire account is read-only", which stopped being
//     the truth the moment the write core landed — this is the "myRights e
//     isReadOnly pasan a decir la verdad nueva" flip of L2-jmap-write §3.
func (s *Server) sessionObject(base string, id *Identity) map[string]any {
	capabilities := map[string]any{
		jmap.CapCore: s.cfg.Limits.CoreCapability(),
		// §1.3.1 of RFC 8621: the mail capability's value in the SESSION
		// capabilities object is an empty object; the per-account limits
		// live in accountCapabilities below.
		jmap.CapMail: map[string]any{},
	}
	accountCapabilities := map[string]any{
		jmap.CapMail: mailAccountCapability(),
	}
	// §2: "urn:ietf:params:jmap:core SHOULD NOT be present" in
	// primaryAccounts; each data capability points at the caller's only
	// account.
	primaryAccounts := map[string]any{
		jmap.CapMail: id.AccountID,
	}
	if s.cfg.Submission {
		// §1.3.2 of RFC 8621: the submission capability's session value is an
		// empty object too, with the account-level truth below.
		capabilities[jmap.CapSubmission] = map[string]any{}
		accountCapabilities[jmap.CapSubmission] = submissionAccountCapability()
		primaryAccounts[jmap.CapSubmission] = id.AccountID
	}
	if s.cfg.Prefs {
		// Moov's vendor preference capability (L3 epic E0). It is advertised
		// in all three places a data capability belongs, mirroring submission
		// exactly: the server-wide capabilities object, the account's
		// accountCapabilities, and primaryAccounts.
		//
		// Advertising it in all three is what makes it DISCOVERABLE rather
		// than secret knowledge a client has to be told about out of band —
		// RFC 8620 §2 is explicit that the session is where a client learns
		// what a server can do. A client that does not recognize the URI
		// ignores three extra keys, which is exactly what §2's extensibility
		// contract asks of it.
		capabilities[jmap.CapPrefs] = prefsCapability()
		accountCapabilities[jmap.CapPrefs] = prefsAccountCapability()
		primaryAccounts[jmap.CapPrefs] = id.AccountID
	}
	if s.cfg.Triage {
		// Moov's vendor triage capability (L3 epic E4): snooze and mute. Same
		// three-place advertisement as preferences, for the same discoverability
		// reason.
		capabilities[jmap.CapTriage] = triageCapability()
		accountCapabilities[jmap.CapTriage] = triageAccountCapability()
		primaryAccounts[jmap.CapTriage] = id.AccountID
	}
	if s.cfg.Sieve != nil {
		// RFC 9661 §1.2.1 puts the values in the account capability; the
		// session-level value is an empty object like the other IETF
		// capabilities' (the RFC defines only per-account properties).
		capabilities[jmap.CapSieve] = map[string]any{}
		accountCapabilities[jmap.CapSieve] = sieveAccountCapability(s.cfg.Sieve)
		primaryAccounts[jmap.CapSieve] = id.AccountID
	}
	if s.cfg.Vacation {
		// RFC 8621 §1.3.3: "The value of this property ... is an empty
		// object" in both places.
		capabilities[jmap.CapVacation] = map[string]any{}
		accountCapabilities[jmap.CapVacation] = map[string]any{}
		primaryAccounts[jmap.CapVacation] = id.AccountID
	}
	if s.cfg.Quota {
		// RFC 9425 §2.1: "The value of this property is an empty object in
		// both the JMAP session capabilities property and an account's
		// accountCapabilities property."
		capabilities[jmap.CapQuota] = map[string]any{}
		accountCapabilities[jmap.CapQuota] = map[string]any{}
		primaryAccounts[jmap.CapQuota] = id.AccountID
	}
	if s.cfg.Filters {
		capabilities[jmap.CapFilters] = filtersCapability()
		accountCapabilities[jmap.CapFilters] = map[string]any{}
		primaryAccounts[jmap.CapFilters] = id.AccountID
	}

	return map[string]any{
		"capabilities": capabilities,
		"accounts": map[string]any{
			id.AccountID: map[string]any{
				"name":                id.Account.Email,
				"isPersonal":          true,
				"isReadOnly":          false,
				"accountCapabilities": accountCapabilities,
			},
		},
		"primaryAccounts": primaryAccounts,
		"username":        id.Account.Email,
		"apiUrl":          base + PathAPI,
		// URI Templates (level 1) with the variables §2 REQUIRES for each
		// endpoint; {type} rides the query string as §2 recommends ("due to
		// potential encoding issues with slashes in content types"). The
		// query parameter is named "type" because that is what handleDownload
		// READS — the template advertised "accept=" for a while, which made a
		// literal substitution yield application/octet-stream for everything
		// (web/README.md gap 3); declared == applied covers URLs too.
		"downloadUrl":    base + "/jmap/download/{accountId}/{blobId}/{name}?type={type}",
		"uploadUrl":      base + "/jmap/upload/{accountId}",
		"eventSourceUrl": base + "/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
		"state":          s.sessionState0(base, id),
	}
}

// submissionAccountCapability is the urn:ietf:params:jmap:submission account
// capability object (RFC 8621 §1.3.2), truthful per the J1 rule:
//
//   - maxDelayedSend: mail.MaxDelayedSend in seconds. §1.3.2 defines it as
//     "the number in seconds of the maximum delay the server supports in
//     sending ... 0 if the server does not support delayed send".
//
//     Through W3 this was 0, and the reasoning was that FUTURERELEASE (RFC
//     4865) is a submission-SERVER capability Postfix does not offer. L3 epic
//     E4 corrected that: the delay is served by this server's own transactional
//     outbox — a scheduled submission is a row whose not_before is in the
//     future, and Postfix only ever sees the message at the scheduled hour, as
//     an ordinary submission. So the honest value is how long this server will
//     hold a message, and advertising 0 while implementing the delay would be
//     as untruthful as the reverse. mail.MaxDelayedSend carries the reasoning
//     for the number itself.
//
//     The W-A3 undo window remains a separate, server-side grace applied to
//     every send; it is still not advertised here, because it is not a
//     client-schedulable delay.
//
//   - submissionExtensions: {}. §1.3.2 scopes it to extensions "the client
//     may use" by putting parameters in the envelope; this server passes none
//     through, so the truthful set is empty regardless of what Postfix's EHLO
//     says to the backend.
func submissionAccountCapability() map[string]any {
	return map[string]any{
		"maxDelayedSend":       int(mail.MaxDelayedSend.Seconds()),
		"submissionExtensions": map[string]any{},
	}
}

// prefsCapability is the server-wide value of Moov's vendor preference
// capability (L3 epic E0).
//
// Unlike the two IETF capabilities, whose session values RFC 8621 §1.3 fixes
// as empty objects, a vendor capability defines its own contents — so this one
// carries what a client genuinely cannot discover any other way: the schema
// version it is talking to, and the fact that /changes is implemented rather
// than declined.
//
// Both keys exist to keep a promise the L2 discipline makes everywhere else in
// this server: never let a client meet an unknownMethod surprise, and never
// advertise something that is not enforced.
//
//   - schemaVersion tells a client which preference vocabulary this server
//     speaks. A newer server with new properties bumps it, and a client that
//     stores preferences locally knows to re-read rather than assume.
//
//     It reads 2 as of the E5/E7/E8/E9b roaming keys (labels, offlineDepth,
//     addressAutocomplete, sendAndArchive, defaultReplyBehavior, signatures).
//     This is the FEATURE-DETECTION contract the bump was taken for on the
//     wire side: a client that patches `signatures` against a server still
//     reporting 1 gets its whole save refused with invalidProperties, so
//     reading this number first is how it avoids sending the patch at all.
//     The value is store.PrefsSchemaVersion, so the number advertised and the
//     number written into every document cannot disagree.
//
//   - maxChangesSupported: true says Prefs/changes really answers, rather than
//     registering only to decline (which is what Mailbox/queryChanges does,
//     deliberately). A client can therefore poll it instead of refetching the
//     whole object.
func prefsCapability() map[string]any {
	return map[string]any{
		"schemaVersion":       mail.PrefsSchemaVersion,
		"maxChangesSupported": true,
	}
}

// prefsAccountCapability is the per-account value of the preference
// capability.
//
// It carries the closed value domains the server ENFORCES, which is the same
// declared == applied rule mailAccountCapability follows for sort options and
// limits: a client builds its settings UI from these lists, so a list that
// disagreed with the validator would produce a control whose every save is
// refused.
//
// The undo-send values are Gmail's exact four (canon §2.3); the server
// additionally clamps to [5, 30] on the send path, so the advertised set and
// the enforced bound cannot disagree by construction.
func prefsAccountCapability() map[string]any {
	headersMin, headersMax, bodiesMin, bodiesMax := mail.OfflineDepthBounds()
	return map[string]any{
		"undoSendSeconds":     mail.UndoSendSecondsChoices(),
		"imagesPolicyValues":  mail.ImagesPolicyChoices(),
		"autoAdvanceValues":   mail.AutoAdvanceChoices(),
		"densityValues":       mail.DensityChoices(),
		"readingPaneValues":   mail.ReadingPaneChoices(),
		"inboxTypeValues":     mail.InboxTypeChoices(),
		"notificationsValues": mail.NotificationsChoices(),
		"themeValues":         mail.ThemeChoices(),

		// --- schema v2 ---
		//
		// The label palette is advertised because it is written down TWICE —
		// here in Go and in web/src/mail/labelPalette.ts — across a boundary no
		// compiler spans. Publishing the server's list means a client whose
		// palette disagreed discovers it in the session object rather than in a
		// refused save (labelColorChoices' comment carries the full reasoning).
		"labelColorValues":           mail.LabelColorChoices(),
		"labelVisibilityValues":      mail.LabelVisibilityChoices(),
		"addressAutocompleteValues":  mail.AddressAutocompleteChoices(),
		"defaultReplyBehaviorValues": mail.DefaultReplyBehaviorChoices(),

		// The numeric caps, so a settings screen can stop a user AT the
		// boundary instead of after a refused save — the same declared ==
		// enforced rule the value lists follow.
		//
		// maxLabels is the durable IMAP keyword ceiling (26), not a product
		// choice: Maildir encodes a keyword as one letter a-z in the filename,
		// so a 27th label cannot survive an index rebuild.
		"maxLabels":         mail.MaxLabelPrefs(),
		"maxSignatures":     mail.MaxSignatureItems(),
		"maxSignatureBytes": mail.MaxSignatureBytes(),
		// SMALLER than maxSignatures × maxSignatureBytes on purpose: the
		// product of the two maxima is the sum the per-item check already
		// enforces, so a cap set there could never bind. Both numbers are
		// advertised because neither describes the limit on its own.
		"maxSignaturesTotalBytes":     mail.MaxSignaturesBytes(),
		"minOfflineHeadersPerMailbox": headersMin,
		"maxOfflineHeadersPerMailbox": headersMax,
		"minOfflineBodies":            bodiesMin,
		"maxOfflineBodies":            bodiesMax,
	}
}

// triageCapability is the server-wide value of Moov's vendor triage
// capability (L3 epic E4).
//
// Like the preference capability, it carries what a client cannot discover any
// other way — and here that is a genuinely load-bearing fact rather than a
// version number: the NAME of the folder snoozed mail lives in.
//
// GC-10 makes snoozing a MOVE to a real IMAP folder, and there is no RFC 6154
// SPECIAL-USE attribute for snoozed mail (internal/sync/snooze.go states why
// inventing one was refused). So a client cannot resolve the Snoozed folder by
// role the way it resolves Trash; it has to know the name. Publishing it here
// means the client reads it from the session instead of hard-coding the same
// string a second time — and if the name ever changes, or a role appears, the
// session tells the truth without a client release.
func triageCapability() map[string]any {
	return map[string]any{
		"snoozeMailboxName": mail.SnoozeMailboxName,
	}
}

// triageAccountCapability carries the per-account triage limits.
//
// maxScheduledSends is the schedule-send cap (canon §2.3's 100) and
// maxDelayedSendSeconds repeats the submission capability's horizon, because a
// client building a "schedule for..." picker needs both and reading one of them
// out of a different capability's object would be a cross-capability
// dependency §1.8 does not promise.
func triageAccountCapability() map[string]any {
	return map[string]any{
		"maxScheduledSends":     mail.MaxScheduledPerAccount(),
		"maxDelayedSendSeconds": int(mail.MaxDelayedSend.Seconds()),
	}
}

// sieveAccountCapability is the RFC 9661 §1.2.1 account capability object,
// truthful per the J1 rule:
//
//   - maxSizeScriptName: 512 octets ("For compatibility with ManageSieve,
//     this MUST be at least 512" — and RFC 5804 §1.6's 128 characters can
//     take exactly 512 bytes in UTF-8, which is what the /set validator
//     enforces).
//   - maxSizeScript: mail.MaxSieveScriptSize, the value the upload path
//     enforces (declared == applied). It mirrors the deployment's Dovecot
//     sieve_max_script_size, which ManageSieve cannot advertise.
//   - maxNumberScripts: null — the Mailcow Dovecot sets
//     sieve_quota_max_scripts = 0 (no limit), and this server imposes none.
//   - maxNumberRedirects: the probed MAXREDIRECTS, else null (§1.2.1's "null
//     for no limit").
//   - sieveExtensions: the probed live list, verbatim and case-sensitive.
//   - notificationMethods / externalLists: probed NOTIFY list; null when the
//     extension is absent (§1.2.1). extlists is not in the deployment's
//     SIEVE list, so externalLists is null.
func sieveAccountCapability(sc *SieveCapability) map[string]any {
	var notify any
	if len(sc.NotificationMethods) > 0 {
		notify = sc.NotificationMethods
	}
	var maxRedirects any
	if sc.MaxRedirects != nil {
		maxRedirects = *sc.MaxRedirects
	}
	return map[string]any{
		"maxSizeScriptName":   512,
		"maxSizeScript":       mail.MaxSieveScriptSize,
		"maxNumberScripts":    nil,
		"maxNumberRedirects":  maxRedirects,
		"sieveExtensions":     sc.Extensions,
		"notificationMethods": notify,
		"externalLists":       nil,
	}
}

// filtersCapability is the server-wide value of Moov's vendor filter
// capability (E6, GC-4). Like the other vendor capabilities it carries what
// a client cannot discover otherwise: the schema version of the rule model
// and the fact that no /changes methods exist on this surface (refresh via
// SSE + /get).
func filtersCapability() map[string]any {
	return map[string]any{
		"schemaVersion":       1,
		"maxChangesSupported": false,
		"verifyPath":          PathForwardingVerify,
	}
}

// mailAccountCapability is the urn:ietf:params:jmap:mail account capability
// object (RFC 8621 §1.3.1), with phase-1-truthful values.
func mailAccountCapability() map[string]any {
	return map[string]any{
		// A message lives in EXACTLY one mailbox: membership mirrors IMAP,
		// where a message is a (folder, UID) pair, and the A6 label model
		// puts Gmail-style multi-membership on keywords instead. Since W1
		// this is enforced — Email/set rejects a multi-mailbox update with
		// invalidProperties — so §1.3.1 requires advertising it: declared ==
		// applied, the same J1 rule as every other limit here.
		"maxMailboxesPerEmail": 1,
		"maxMailboxDepth":      nil,
		// "MUST be at least 100" (§1.3.1). 255 matches what Dovecot accepts
		// for one mailbox name component in practice; with no Mailbox/set in
		// phase 1 nothing can exceed it, and the write phase will enforce it.
		"maxSizeMailboxName": 255,
		// Enforced by Email/set create since W3 (mail.maxAttachmentsBytes —
		// a test pins the two together); 25 MB mirrors the Mailcow message
		// size ceiling the submission path inherits.
		"maxSizeAttachmentsPerEmail": 25_000_000,
		// The sort properties Email/query actually accepts — exactly what
		// mail.translateSort implements, no more (J1's declared == applied
		// rule, applied to comparators).
		//
		// "receivedAt" is the one RFC 8621 §4.4.2 says MUST be supported, and
		// it is the store's native date order (S3 shape #1, 9.3 ms p95).
		//
		// "relevance" is not an RFC 8621 property name: §4.4.2 permits extra
		// ones ("The server MAY support sorting based on other properties as
		// well. A client can discover which properties are supported by
		// inspecting the account's capabilities object"), and this server
		// deliberately does NOT name it after any standard property, because
		// what it implements is relevance within a bounded recent window
		// (store.RankCandidateWindow = 200, S3 mitigation #102) rather than a
		// general relevance sort. Advertising it under a standard name would
		// promise the unbounded version that measured 892 ms p95.
		//
		// hasKeyword IS advertised (J4): it is served over the bounded result
		// window rather than by an index, which costs nothing extra because the
		// window was already fetched and the store now returns each row's
		// keywords. It is what a real client needs — Bulwark opens every folder
		// with [hasKeyword $pinned, receivedAt] — and the refusal was a genuine
		// §4.4.2 conformance gap, not a client quirk.
		//
		// The rest of the §4.4.2 SHOULD list (size, from, to, subject, sentAt
		// and the inThread variants) stays absent because the store has no index
		// that orders by them — advertising them would be the lie this array
		// exists to avoid.
		"emailQuerySortOptions": []string{
			mail.SortReceivedAt,
			mail.SortRelevance,
			mail.SortHasKeyword,
		},
		// True since W2: Mailbox/set create accepts parentId:null (a
		// top-level folder). The flag lagged the feature by one epic —
		// noticed and corrected during W3's session-truth pass, with the
		// truth test updated alongside.
		"mayCreateTopLevelMailbox": true,
	}
}

// baseURL resolves the external base URL for session links: configuration
// when set, otherwise derived from the request (scheme from the fronting
// proxy's X-Forwarded-Proto or the connection itself, host from Host).
// Deriving from headers is safe here: the URL only feeds the caller's own
// response, so a forged header misleads no one but its sender.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf == "https" || xf == "http" {
		scheme = xf
	}
	return scheme + "://" + r.Host
}

// sessionState computes the caller's session "state" string for API
// responses (Response.sessionState, §3.4 — "the current value of the 'state'
// string on the Session object").
func (s *Server) sessionState(r *http.Request, id *Identity) string {
	return s.sessionState0(s.baseURL(r), id)
}

// sessionState0 fingerprints everything the Session object is built from, so
// the string changes exactly when the object would (§2: "If the value of any
// other property on the Session object changes, this string will change"):
// the account row (email/state/updatedAt), the URLs' base, the advertised
// limits, and the server build (a deploy may change capabilities).
func (s *Server) sessionState0(base string, id *Identity) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%d\x00%+v\x00%s",
		base,
		id.Account.ID,
		id.Account.Email,
		id.Account.State,
		id.Account.UpdatedAt.UTC().UnixNano(),
		s.cfg.Limits,
		version.Get().Version,
	)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
