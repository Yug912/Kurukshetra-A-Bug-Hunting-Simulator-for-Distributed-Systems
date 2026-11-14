# Brain progress

## Status

M1–M5 done; M6 (`kuru serve`) next. Person A's engine is not on `main` yet, so engine-dependent acceptance steps are pending. Work happens on branch `anushka` (Anushka's own branch, standing in for `feat/brain`).

## Milestones

- [x] **M1 Toy Raft KV** (2026-11-14). `go test -race ./targets/toykv` passes: FSM, transport round trip (4 RPCs, duplicate request, heartbeat fast path, errors), in-process 3-node failover, lease clock logic. `docker build -t kuru/toykv -f targets/toykv/Dockerfile .` builds a 17 MB image. `go test -tags integration ./targets/toykv` passes (compose up, write/read, `docker kill` leader, new leader in 1.34 s, data still readable).
- [x] **M2 Workload `kv`** (2026-11-14). `go test -race ./pkg/workload` passes: well-formed history, op mix within ±8 %, deterministic per-client choices, ok/fail/info classification under chaos, rate limit (100 ops/s → 35–60 ops in 500 ms), cancel stops in < 300 ms with interrupted ops as `info`, final-read retries. `go test -tags integration ./pkg/workload` passes against compose: 10 s of traffic plus final reads, 1034 pairs, every ok read returns a written value or "".
- [x] **M3 Checkers** (2026-11-14). `kuru check <copy of testdata/sample-run> --checkers linearizable-kv,durability` → linearizable-kv FAIL, "stale read of k0 on n1: got c0-1, expected c1-1", bad ops 4,5, `linearizability.html` written; durability PASS; exit 1. Table tests cover valid concurrent, stale read, info write both ways, never-completed write, info read dropped, CAS ok / wrong expect / 409 ignored / info both ways, overlap blame, and durability lost / overwritten / concurrent overwrite / info overwrite / lost CAS / no final reads → UNKNOWN. A 5,000-op random valid history checks PASS in 7 ms, and FAIL after one corrupted read.
- [x] **M4 Timeline viewer v1** (2026-11-14). `cd web/timeline && npm test` passes 12 Vitest tests: JSONL parser, message pairing (deliver/drop/delay/dup on the sample), op pairing + bad-op marking, partition/crash/pause intervals, skew markers, viewport math, and a 50,000-event synthetic run that builds its model in < 1 s and answers window queries in < 5 ms. `npm run build` passes (tsc + vite, 166 kB JS). Screenshot of `?run=sample` in [docs/timeline-sample.png](timeline-sample.png): partition band n1 | n2,n3 (120–600), hatched crash of n3 (700–800), red ✕ drops, the amber delayed request_vote, the dashed duplicate, and op 4/5 (`R k0→c0-1` on c2) outlined red. `?run=synthetic` renders 50k events, decimating to ≤ 1,500 plain + 600 special messages with a "zoom in" note.
- [x] **Checkpoint #1** (2026-11-14): `origin/main` unchanged (c8aa64f), so no rebase was needed. All Go, integration and web tests pass. Pushed to `anushka`. No PR opened: `gh` is not installed and the human asked for work to stay on their branch. Added `examples/toykv-{correct,stale-read,lost-write,lease-clock}.yaml` (parse-tested). The seeds 1–20 sweep with `kuru run` waits for Person A's engine.
- [x] **M5 Shrinker** (2026-11-14). `go test -race ./pkg/shrink` passes: ddmin on synthetic predicates (single culprit, pair, adjacent pair, triple, none needed) and the fake-faults acceptance. `examples/fake-faults.yaml` seed 1 (8 faults) → `minimal: 2 faults (from 8) — crash n3 at step 195 for 1; partition n1 | n2,n3 at step 420 for 1 — 36 runs (message faults off)`, within the 60-run budget. `minimal.json`, `shrink.log` and `<run>-min` (verdict FAIL, `faults_from` → minimal.json) are written; parallel=4 gives the same result; budget exhaustion, non-FAIL refusal and cancellation are tested. `kuru shrink <dir>` is tested end to end through cobra. Shrinking a real toykv-stale-read run waits for Person A's engine.
- [ ] M6 kuru serve
- [ ] M7 Viewer v2
- [ ] M8 Real-world target
- [ ] M9 Docs and demo

## Decisions

1. **Branch.** The human asked for all work on `anushka` instead of `feat/brain`. Same rules otherwise.
2. **toykv Dockerfile uses `golang:1.26`**, not 1.22, because `go.mod` requires Go 1.26 (SETUP_NOTES deviation 1).
3. **Uncertain write outcomes are not 503.** If `Apply` was accepted but the commit wait times out, or leadership is lost (`ErrLeadershipLost`), toykv answers `504`/`500` ("unknown"). The workload records those as `info`. Only `ErrNotLeader` (→ 421) and `ErrEnqueueTimeout`/shutdown (→ 503) are "definitely not applied". A 503 for an entry that may still commit would make the correct build fail linearizability.
4. **hashicorp/raft logging** goes through `Config.LogOutput`/`LogLevel` (not an hclog import), so `go.mod` stays untouched.
5. **BUG_LEASE_READ lease** = last successful AppendEntries response time (on the skewable clock) + 300 ms. The current Raft state is deliberately not checked; that is the bug.
6. **No PreVote.** The transport does not implement `WithPreVote`, so hashicorp/raft disables pre-vote. The spec lists only the four RPCs.
7. **Workload.** One shared limiter spaces ops evenly (`1s/ops_per_sec`). CAS `expect` = the last value this client saw for the key ("" if none). Final reads run as their own client id `= cfg.Clients`. The read-after-retry pause is 400 ms (5 attempts in 2 s).
8. **Compose host ports** are configurable (`TOYKV_PORT_N1..3`, default 18081–18083), so the toykv and workload integration tests can run in parallel.
9. **Local toolchain.** Go 1.26.11 in `~/.local/go`. Docker comes from Colima 0.10.3 + Lima 2.3.0 (vz VM) with the static Docker CLI 29.9.3 and Compose 5.7.1 under `~/.local`; no Homebrew or admin rights were available.
10. **linearizable-kv** uses porcupine's `NondeterministicModel` (state = register value; an info write/CAS yields {unchanged, new}), partitioned by key. Bad ops: on the failing key, the op left out of the longest partial linearization that returns first is the culprit; it is blamed with every other un-linearized op overlapping it in time, as invoke + completion indices. "expected" = the state(s) after that partial linearization. Every failing key adds its bad ops; the reason names the first key plus "(and N more key(s))".
11. **durability "overwritten" rule.** The spec says "no other write invoked after W completed". That would blame W when a *concurrent* write is linearized after it, a false FAIL. I use "no other write/CAS on the key whose return (∞ for info) is after W's invoke", i.e. any write that could be ordered after W excuses it. Any ok read returning W's value (not just later ones) proves W happened, since values are unique. Keys with no ok final read are skipped.
12. `kuru check` exits through `os.Exit(1|2)` after printing the table (cobra's error path would print usage text).

13. **Web app.** React 18.3.1, Vite 5.4.21, Vitest 2.1.9, TypeScript 5.9.3: mature versions that run on CI's Node 20. No d3 (the step axis is one linear map). `npm run dev`/`build` first run `scripts/brain-copy-sample.sh` (pre-scripts), so `?run=sample` always works. `public/sample-run` is gitignored. Vite proxies `/api` to `kuru serve` at 127.0.0.1:7777 during `npm run dev`.
14. **Timeline performance.** Messages and ops are sorted once. A window query is a binary search from `start - maxSpan`. When a window holds more than 1,500 plain messages (or 600 drops/delays/dups), they are decimated evenly with an on-screen note. `?run=synthetic` loads a generated 50k-event run for checking this by hand.

15. **Shrinker.** Each round's candidates run as one batch (up to `--parallel` at once) and the first failing candidate *in order* wins, so the result does not depend on scheduling. Trials run in `<run parent>/.shrink-XXXX/trial-NNN` (same filesystem, removed afterwards). Cached and over-budget trials cost nothing and are logged. `<run-dir>-min` is the trial run that proved the minimal schedule fails (renamed into place, with `faults.json` = minimal and `meta.faults_from` → minimal.json); it is re-run only if it was never tried on its own. That beats a fresh re-run because it is guaranteed to FAIL. Phase 1 decides `NoMessageFaults` once, for all later trials. No randomness is needed, so `rng.StreamShrink` is unused.
16. **`kuru shrink` runner choice.** `--runner` if given, else `meta.json`'s runner if it is registered (so a `fake-faults` run shrinks with `fake-faults`), else the global default `docker`.

## Contract change requests

- None yet. (Note: `gofmt -l` reports `pkg/core/core.go` as not gofmt-clean with Go 1.26.11. It is a shared file, so I left it alone.)

## Known issues

- golangci-lint 2.15 reports errcheck/staticcheck findings in **shared** files (`cmd/fake-node`, `cmd/gen-sample`, `cmd/kuru/version.go`, `pkg/core`, `pkg/fakes`, `pkg/scenario`, `pkg/trace` tests), so CI's lint step fails on `main` too. My owned paths are lint-clean.

## Next step

M6: `cmd/kuru/serve.go`: `/api/runs`, `/api/runs/{scenario}/{run}/{file}` (allow-list, traversal and symlink protection), `/api/summary/{scenario}`, static web dir with SPA fallback and a missing-dir message, CORS for localhost:5173, Go tests.
