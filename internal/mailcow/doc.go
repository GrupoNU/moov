// Package mailcow is a minimal client for the Mailcow admin API, used by
// provisioning to mint the app password Moov authenticates to Dovecot with.
//
// # Scope
//
// Deliberately three operations — create an app password, delete one, read a
// mailbox — and nothing else. Moov's contract with Mailcow (ADR-001 §4) is that
// Mailcow is never modified and barely touched: mail flows over IMAP, SMTP and
// Sieve, and this API is used once per account, at provisioning time. A fuller
// client would be surface area for a dependency we want as thin as possible.
//
// # Endpoints
//
// Verified against a live Mailcow (commit 281cf93, 2026-03-31) on the Grupo NU
// mail VPS. The API is the one documented at data/web/api/openapi.yaml in
// mailcow-dockerized; the request shapes below come from that spec AND from
// reading data/web/json_api.php, because the two differ in ways the spec does
// not make obvious.
//
//	POST   /api/v1/add/app-passwd            create
//	POST   /api/v1/delete/app-passwd         delete (note: POST, not DELETE)
//	GET    /api/v1/get/app-passwd/all/{mbox} list
//	GET    /api/v1/get/mailbox/{mbox}        mailbox details
//
// Authentication is the X-API-Key header on every request. The key must be a
// read-write key: mailcow rejects any non-GET request from a read-only key.
//
// # Shapes worth knowing, because they are surprising
//
// Delete is a POST whose body is a bare JSON ARRAY of ids — `["1"]` — not an
// object. json_api.php assigns the raw request body to $_POST['items'] for the
// delete action, so an object body is silently misread. Create, by contrast,
// takes a JSON OBJECT, and its `protocols` field is an array of names
// ("imap_access", "smtp_access", "sieve_access"); a protocol absent from the
// array is stored as denied. Omitting `protocols` entirely creates an app
// password with NO access at all, which authenticates against nothing —
// upstream issue #4588. [Client.CreateAppPassword] always sends the array
// explicitly for that reason.
//
// # The response shape problem
//
// Mailcow's API answers HTTP 200 for failures. A rejected create returns 200
// with a body of {"type":"danger", ...}; a successful one returns 200 with
// {"type":"success", ...}. The status code alone is not a result, so every
// response body here is parsed and the `type` field is what decides. Responses
// are also inconsistently shaped — sometimes an object, sometimes an array of
// them — which [apiResult] absorbs.
//
// Worse for our purposes: the create response does NOT return the id of the row
// it just created. Getting the id — which we need in order to delete the app
// password again when an account is removed — takes a follow-up list call
// matching on the app name. That is why [Client.CreateAppPassword] mints a
// name carrying a random suffix: it makes the follow-up lookup unambiguous even
// if two provisioning runs race for the same mailbox.
//
// # Force IPv4 (spike S1, finding H5)
//
// Mailcow's API enforces an IP allowlist. On a dual-stack host a container may
// reach nginx over IPv6 and be rejected as an unlisted address, while the same
// request over IPv4 succeeds — the failure mode S1 hit and documented as H5.
// The client therefore dials IPv4 only by default ([Config.ForceIPv4] defaults
// to true through [Config.Normalize]), which costs nothing in the Docker
// network Moov actually runs in and removes an entire class of "works on my
// host" provisioning failures. It can be turned off for an IPv6-only
// deployment.
//
// The related deployment note is that the allowlist must contain whatever
// address Moov's container presents. In the Grupo NU deployment Moov reaches
// the API at the nginx container inside mailcowdockerized_mailcow-network,
// which is also the only path that does not leave the host.
//
// # What this package will not do
//
// It does not retry writes. A create that fails with a network error after the
// request was sent may or may not have created a row, and a blind retry would
// mint a second app password nobody is tracking — a credential leak in the most
// literal sense. Reads (GET) are safely repeatable and the caller may retry
// them; writes surface the error and let provisioning decide. See
// [Client.CreateAppPassword].
package mailcow
