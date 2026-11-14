# Engine progress

## Status

M1–M9 done (2026-11-14). Nothing pushed: the humans push (see Decisions). Waiting for Person B's work on main to run the toy KV checks of checkpoints #1/#2 and the final loop.

## Milestones

- [x] **M1 Docker controller** (2026-11-14) — `go test -tags integration ./pkg/docker`: starts 3 `kuru/fake-node`, health 200, kill+restart n2 (same host ports), pause/unpause n3, CopyLogs, `CleanupRun` leaves 0 containers/networks/volumes.
- [x] **M2 Proxy (http)** (2026-11-14) — unit tests (httptest, `-race`): sequential per-link IDs, body/headers intact, Drop → 504, Duplicate hits target twice, blocked link → `msg_drop reason=partition`, sender-gone still recorded, Close idempotent. Integration: 3 fake-nodes via proxy for 3 s pass-through → 382 messages, 0 sends without exactly one deliver.
- [x] **M3 Scheduler** (2026-11-14) — unit tests with a fake bus (`-race`): `Decide` identical across 20 runs for 900 IDs; identical ops across 20 shuffled arrival orders; delay released on the exact step; blocked → partition drop; `NoMessageFaults` delivers all; faults fire/heal sorted.
- [x] **M4 Fault plan + executor** (2026-11-14) — `Plan` identical for same seed, differs across seeds, `Validate` passes for seeds 1–100 at n = 1, 2, 3, 5, 7 (incl. dense configs that force no-overlap pushes and minority skips); toggling one fault type does not reshuffle others; executor tested with fake proxy/docker/HTTP (404 → `unsupported=true`, HealAll idempotent).
- [x] **M5 Engine + `kuru run`** (2026-11-14) — `kuru run examples/fake-smoke.yaml --seed 1` → `seed=1 verdict=PASS checks=fake-pass:PASS faults=9 dir=runs/fake-smoke/1`; trace has partitions (incl. bridge), crashes/restarts, pause/resume, clock skews, drops, dups, delays; 0 undecided messages. `scripts/engine-determinism.sh` → faults.json identical, 99.59% identical decisions on 488 shared IDs. Ctrl-C mid-run → verdict ERROR, exit 2, 0 containers/networks/volumes left. Integration tests: fake-smoke PASS + all files, cancel cleans up, missing image → ERROR result + cleanup.
- [x] **M6 Replay** (2026-11-14) — `scripts/engine-replay-check.sh` (run + replay of fake-smoke seeds 1–5), 8 rounds = 40 pairs: all ≥ 95% (lowest 96.2%, 35/40 at 99.6–100%), fault events 17/17…21/21 exact, verdict reproduced every time. Unit tests: matcher cases (on time, early hold, startup counterpart, late-nearest, skip, extra), bounded freeze, no wait on blocked sender, forced decisions/timing, guide building, fidelity scoring, NextDir. Integration: `TestReplayFidelity` (seed 3 ≥ 95%, `replay_of`, `replay-fidelity.json`). Determinism re-checked: fault plan identical, seed-driven decisions 100% (seeds 1–3), all decisions 95.7–100%.
- [x] **M7 Explorer + cleanup** (2026-11-14) — `kuru explore examples/fake-faults.yaml --runner fake-faults --runs 500 --parallel 8` → 500 runs in 0.77 s, 266 pass / 234 fail, summary.csv 501 lines, only FAIL dirs kept, exit 1. `kuru explore examples/fake-smoke.yaml --runs 12 --parallel 4` (Docker) → 12/12 PASS in 17 s (~42 runs/min), 0 containers/networks/volumes left. Ctrl-C mid-explore → no new seeds, 4 in-flight runs cleaned up and reported as interrupted, summary printed, 0 left. `kuru run --keep` then `kuru cleanup` → lists and removes 3 containers, 1 network, 3 volumes; second cleanup removes nothing. Unit tests (`-race`, ×5): CSV header/rows/append, passing dirs deleted and failing kept, exact `--stop-after-fails`, cancel stops seeds, progress line, unknown runner.
- [x] **M8 Stream mode (tcp)** (2026-11-14) — unit tests (`-race`, ×3) with a local TCP echo server: 5 chunks delivered in reverse order arrive intact and in order (FIFO per connection), drop → client sees a reset + `msg_drop reason=reset`, partition resets the existing connection and refuses new ones while the other direction keeps working, duplicate = single delivery, reply direction recorded with `dir=reply`. `examples/engine-tcp-smoke.yaml` (fake-node, whose peer HTTP runs over TCP): `kuru run … --seed 1` → PASS, 419 tcp chunks, 410 recorded replies, 6 resets, 61 refused connections during partitions; integration test `TestRunTCPMode`.
- [x] **M9 Hardening, performance, docs** (2026-11-14) — `kuru explore examples/fake-smoke.yaml --runs 50 --parallel 4` (Docker) → 50/50 PASS in 69 s, 0 containers/networks/volumes left. `TestNoGoroutineLeaks` (3 engine runs, goroutine count back to baseline). Every `Close` idempotent (proxy, executor HealAll, cleanup). Containers start in parallel; health checks back off (20 ms → 500 ms); `step_ms` configurable; images reused (never pulled); per-phase timings in `logs/engine-timings.json`. `golangci-lint run --build-tags integration ./...` → 0 issues (shared code fixed in a separate commit on main). `docs/engine.md` complete: architecture diagram, run phases, proxy, scheduler, faults, determinism model and limits, replay, explore, stream mode, run folder, CLI reference, troubleshooting, README snippet.

## Decisions

- Docker runtime on the dev Mac is Colima (`brew install colima docker docker-buildx`, `colima start`). `docker.New()` falls back to `~/.colima/default/docker.sock` (and Docker Desktop / OrbStack / Rancher sockets) when `DOCKER_HOST` is unset and `/var/run/docker.sock` is missing. `host.docker.internal:host-gateway` reaches the macOS host from Colima containers; `KURU_PROXY_HOST` overrides it if needed.
- Host ports are picked up front (free ports on 127.0.0.1) and bound explicitly instead of `HostPort: "0"`, because Docker re-assigns random ports on restart; with explicit ports a crashed node keeps its ClientURL/PeerHostAddr. Start retries on port conflicts.
- `CleanupAll` returns `(docker.Removed, error)` so `kuru cleanup` can print what it removed.
- Startup: while waiting for `/kuru/health`, peer traffic is passed through at step 0 (no faults) so Raft targets can elect a leader. The logical clock starts when the workload starts.
- Final reads run while a background quiet drain keeps the clock ticking, so reads that need peer messages still complete.
- Delivery and duplicate trace events are emitted synchronously in the shuffled order, and the network I/O runs in goroutines (`Proxy.DeliverAsync`/`DuplicateAsync`), so a slow or paused node never stalls the clock and trace order is deterministic.
- A message on a blocked link is dropped with `reason=partition` without consuming a PRNG draw; a delayed message released into a partition is dropped the same way.
- `Decide` always draws drop/dup/delay floats and the delay length in a fixed order from `Keyed(StreamMessages, id)`.
- Message IDs are sorted by link then numerically (`n1-n2-2` < `n1-n2-10`).
- Fault plan: each fault type gets its own generator seeded from `Stream(StreamFaults)` in a fixed order (partition, crash, pause, clock_skew), so tuning one type never reshuffles another. Partition strategies: isolate-one (always), bridge (n ≥ 3; `Groups` = two sides, `Nodes` = [bridge node] which can talk to both), majority-minority (n ≥ 5; below that it equals isolate-one). Crash and pause share the MaxDown = floor((n−1)/2) budget; a node already down is never taken down again. Clock skew lasts until HealAll (DurationSteps 0).
- A restarted node's clock offset is forgotten (it restarts with `KURU_CLOCK_OFFSET_MS=0`).
- Non-fatal errors (deliveries to crashed/paused nodes) go to `logs/engine.log` only; the console shows a count.
- Phase timings go to `logs/engine-timings.json` (meta has no field for them).
- `docker/go-connections/nat` and `docker/docker/errdefs`, `pkg/stdcopy` are used directly; they are already in go.mod (as indirect deps), so go.mod is untouched.
- Replay design (details in docs/engine.md): matching by link + timing instead of a fixed counter; clock pauses for late recorded messages (≤ 30 ticks per step, not 5 as the prompt suggests: fake-node timers drift by up to one 25-step ping period after restarts/pauses, and 5 ticks cannot re-lock the clock); early messages held ≤ 50 steps; never wait for a sender with a held message (synchronous senders would deadlock the clock); first-message startup counterparts get `-sx<k>` IDs and pass through like the original's startup traffic; unmatched messages get `-x<k>` IDs and the PRNG decision.
- Startup traffic has its own IDs `<from>-<to>-s<n>`, set when the proxy is created (before containers boot); run-phase IDs start at 1 when the clock starts.
- `run_start` meta records `no_message_faults=true` so a replay of a shrunk run keeps message faults off.
- Pushing: the prompt says to push after each milestone and open PRs at checkpoints; the human (Yug) asked that nothing be pushed unless they ask, so all work is committed locally on `feat/engine` (and the two shared fixes on `main`).
- Determinism metric: `engine-tracediff` reports seed-driven decisions (threshold, expected 100%) and all decisions (informational). Messages cut by a partition or decided in the quiet period depend on node timers (when the message arrives relative to a fixed partition step), which the determinism model explicitly does not control; the prompt's "≥ 99% identical decisions" holds for seed-driven decisions (100%), overall is 95–100%.
- Port allocation verifies a port is free on both 127.0.0.1 and 0.0.0.0, and the proxy reserves its ports; on macOS a Docker/Colima port forward on 127.0.0.1:P silently shadows the proxy's 0.0.0.0:P (seen once: a whole link bypassed the proxy). The engine warns about links with no run-phase traffic.
- Explore: runs interrupted by Ctrl-C are not results (not in the CSV, their dirs removed); a worker takes its next seed only after its result is counted, so `--stop-after-fails K` stops at exactly K; exit code 0 no failures, 1 any FAIL, 2 errors only. The failing-seed list is capped at 20 (the CSV has all). The suggested next commands are `kuru shrink <dir>` and `kuru replay <dir>` (shrink syntax is Person B's; adjust if it differs).
- tcp mode: a partition acts at the connection level (refuse/reset, recorded as `note` events with `reason=partition`), so the scheduler's "cut by partitions" counter stays 0 there; reply chunks get IDs `<to>-<from>-r<conn>.<n>` and are ignored by replay matching and determinism diffs (they are piped, not decided). Dial failures to a crashed node are `note` events.
- `pkg/replay` holds the pure parts (guide, fidelity, decision diff); the engine imports it, never the reverse.
- `scripts/engine-tracediff/` is a small Go command used by `scripts/engine-determinism.sh` (no jq/python dependency).

## Contract change requests

None open. Fixed directly on main as small separate commits (shared code, no contract change):
1. "setup: fix fake-node Dockerfile to build a static binary" — `CGO_ENABLED=0` only applied to `go mod init`, so the image could not start.
2. "setup: make shared code golangci-lint clean" — CI's lint job would have failed on main; also two real fixes: gen-sample's test handles `WalkDir` errors, and the `fake-put` workload records an unparseable 200 reply as `info` instead of a read of "".

## Known issues

- Messages parked after the last quiet step (between final reads and proxy close) get no decision event.
- Replay fidelity on targets with randomized timers (Raft election timeouts) is expected to be lower than on fake-node; it is reported, not guaranteed.

## Next step

1. Humans: push `main` and `feat/engine`, then open the PR `feat/engine → main` ("Engine checkpoint", engine merges first).
2. After Person B's work is on main: `git rebase origin/main`, then `kuru run examples/toykv-stale-read.yaml --seed <a few seeds>` with the bug on and off (expect FAIL / PASS) and record it here; then the full loop explore → shrink → replay → serve on toy KV, and record replay fidelity on toy KV.
3. Final: rebase, full test suite + integration, push, PR "Engine v0.1.0".
