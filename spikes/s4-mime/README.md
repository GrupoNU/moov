# Spike S4 — pathological MIME corpus + dual-parser harness

**Question:** do `emersion/go-message` and `jhillyerd/enmime` fail on *different*
broken mail — enough to justify running both — and can either be made to panic or
hang by hostile input?

**Answer:** yes and no, respectively. See [`RESULTS.md`](RESULTS.md).

## What is here

| Path | What it is |
|---|---|
| `gen/` | Deterministic generator that emits the corpus. Committed output lives in `testdata/mime-corpus/`. |
| `harness.go` | Runs the full corpus through both parsers, under a watchdog and a `recover()`. |
| `RESULTS.md` | Findings, the full per-case matrix, and the actions for the engine. |
| `results.json` | Raw per-case data: outcomes, error strings, defect lists, timings, allocations. |

The corpus itself is **not** here — it lives at `testdata/mime-corpus/` because it
outlives this spike as the parser's permanent regression suite (`testdata/` is the Go
convention). This directory is the tooling that produced and exercised it.

## Running

```bash
# Regenerate the corpus (must be byte-identical every run)
cd gen && go run . -out ../../../testdata/mime-corpus

# Run both parsers over it
go run . -corpus ../../testdata/mime-corpus -timeout 10s
```

Requires **Go ≥ 1.25** (enmime v2 does). Under a memory cap, which is worth doing
given the nesting bombs:

```bash
docker run --rm --cpus=2 --memory=2g \
  -v "$PWD/../..:/w" -w /w/spikes/s4-mime golang:1.25-bookworm \
  go run . -corpus /w/testdata/mime-corpus
```

## Reading the harness

The outcome taxonomy is ordered worst-first — `panic` and `timeout` above `error` —
because for a sync engine a crash and a hang are categorically worse than a clean
failure. A clean failure is handled: mark `parse_status='failed'`, keep the raw blob,
move on. A hang holds a folder open forever.

Two things in `harness.go` are deliberate and easy to break:

- **Harness limits are not parser limits.** `maxDepth`/`maxParts` guard the harness's
  own walk. They must stay above the deepest corpus case or they mask parser behavior
  and surface as false parser failures — which happened once during this spike and is
  documented in RESULTS.md §5. Anything attributable to them is prefixed `harness `.
- **Partial body reads are kept, not discarded.** `io.ReadAll` returns decoded bytes
  *alongside* its error; throwing them away on error converts a partial decode into
  total data loss. This is the trap the production parser must also avoid
  (RESULTS.md §4.2).

## Next

The corpus is now the parser's test suite. Wire it into CI alongside the JMAP
TestSuite so a library upgrade cannot silently change parse behavior — the findings
in RESULTS.md are specific to go-message v0.18.2 and enmime v2.4.1.
