# Security Policy

Moov Mail handles email: message content, credentials, and the connection to a
production mail server. A vulnerability here is not abstract. We take reports
seriously and we would rather hear from you early than read about it later.

## Supported versions

Moov Mail is **pre-alpha**. There is no released version and no supported
version yet — the sync engine is under construction and nothing is deployed for
end users. This policy is published now so that a channel exists from the first
day there is code to attack.

Once there are releases, this section will list which ones receive security
fixes.

## Reporting a vulnerability

**Please do not open a public issue, pull request, or discussion for a security
vulnerability.**

Report it privately through **GitHub Security Advisories**:

1. Go to https://github.com/GrupoNU/moov/security/advisories
2. Click **Report a vulnerability**
3. Describe the issue

This creates a private advisory visible only to you and the maintainers, where
we can discuss, prepare a fix and coordinate disclosure. It also lets us credit
you properly and request a CVE when one is warranted.

If GitHub Security Advisories is not available to you for any reason, write to
**security@gruponu.com** with `[moov]` in the subject.

### What to include

The more of this you can provide, the faster the fix:

- What kind of issue it is, and what an attacker gains.
- Which component: sync engine, JMAP layer, PWA, deployment configuration.
- The affected version, commit or branch.
- Step-by-step reproduction, ideally with a minimal proof of concept.
- Any configuration required for the issue to be exploitable.
- Whether it is already public anywhere.

If you have a suggested fix, it is welcome — but please send it privately rather
than as a public pull request, since a fix commit often discloses the bug.

## What to expect

| Stage | Target |
|---|---|
| Acknowledgement of your report | within 3 business days |
| Initial assessment (severity, whether we can reproduce it) | within 7 business days |
| Fix or a concrete mitigation plan | depends on severity; critical issues take priority over all other work |

We will keep you updated as the fix progresses, tell you when it ships, and
credit you in the advisory unless you prefer to stay anonymous.

## Disclosure

We follow coordinated disclosure. We ask that you give us a reasonable window to
ship a fix before publishing — 90 days is our default, shorter if a fix lands
sooner, and we will tell you if a specific issue genuinely needs longer. If a
vulnerability is being exploited in the wild, tell us and we will move
immediately.

We publish an advisory when the fix is released, describing the issue and its
impact so that operators can judge their own exposure.

## Scope

**In scope** — anything in this repository:

- The sync engine, the JMAP layer, the PWA and their dependencies as we use them.
- Credential handling: the app-password provisioning flow, encryption at rest,
  key handling.
- Anything that lets one account read or modify another account's mail.
- HTML sanitization bypasses, script execution in rendered mail, and the image
  proxy (SSRF, request smuggling).
- Injection into IMAP, SMTP or Sieve commands.
- Configuration we ship that is insecure by default.

**Out of scope:**

- Vulnerabilities in Mailcow, Dovecot, Postfix or Rspamd themselves — report
  those to their own projects. If Moov's *use* of them is what creates the
  problem, that is in scope and we want to hear it.
- Attacks requiring an already-compromised server or physical access.
- `docker-compose.dev.yml` and other files explicitly marked as
  development-only, whose weak credentials are deliberate and documented.
- Missing hardening headers with no demonstrated impact, and automated-scanner
  output with no analysis attached.
- Social engineering of maintainers or users.

## Security properties we intend to hold

These are the invariants we consider security-relevant. If you can break one,
that is a vulnerability, even if nothing else looks wrong:

- **The user's own password is never persisted.** It is used once, for an IMAP
  login that validates it, and then discarded. What is stored is a scoped
  application password.
- **Stored credentials are encrypted with AES-256-GCM, with the master key held
  outside the database.** A database dump alone must not yield usable
  credentials.
- **Mail HTML is sanitized in three independent layers** — server-side, in the
  client, and inside a sandboxed iframe under a strict Content Security Policy
  with no script execution.
- **Remote content in mail is proxied**, never fetched directly by the browser,
  with HMAC-signed URLs and SSRF protection on the proxy.
- **Certificate verification is always on.** There is no global switch to
  disable it.
- **Mailcow's mail store is never touched directly.** Everything goes through
  IMAP, SMTP, Sieve and the API.

Thank you for helping keep Moov and its users safe.
