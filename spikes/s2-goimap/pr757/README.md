# PR #757 validation probe

Validates [go-imap PR #757 "imapclient: support QRESYNC"](https://github.com/emersion/go-imap/pull/757)
against a real Dovecot, rather than only assessing the diff on paper.

This is a **separate Go module** on purpose: it builds against a *patched*
go-imap checkout, while the parent spike pins the stock upstream
pseudo-version. Keeping them apart stops a `replace` directive from leaking
into the parent module's dependency graph.

## What it asserts

1. `Client.Enable(imap.CapQResync)` is accepted (it is refused by stock
   branch-v2 — see `RESULTS.md`, T2e).
2. `Select(..., &imap.SelectOptions{QResync: ...})` issues
   `SELECT (QRESYNC (uidvalidity modseq))` and the server replays the delta.
3. `SelectData.VanishedUIDs` reports the UID expunged while "offline".
4. `FetchOptions{ChangedSince, Vanished: true}` works and fires
   `UnilateralDataHandler.Vanished(uids, earlier)`.

## Setup

Clone branch `v2`, pin the tip this spike validated, and apply the PR:

```bash
git clone --branch v2 https://github.com/emersion/go-imap.git goimap
cd goimap
git checkout f68ef419e622a283e0cf8ddab4498b84f9bd038d
curl -sL https://patch-diff.githubusercontent.com/raw/emersion/go-imap/pull/757.diff | git apply
```

`goimap/` is intentionally not committed — it is a third-party checkout.

## Run

```bash
docker run --rm --network mailcowdockerized_mailcow-network \
  -v "$PWD":/app -w /app \
  -e IMAP_HOST=dovecot -e IMAP_PORT=143 \
  -e IMAP_USER=moov-test@atmosfera.cloud -e IMAP_PASSWORD=<secret> \
  golang:1.24-bookworm go run .
```

Expected: `PR757 VALIDATION: 0 failure(s)`.

A ready-to-run copy (with `goimap/` already patched) is left on the VPS at
`/root/moov-s2-pr757`.
