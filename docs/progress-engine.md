# Engine progress

## Status

M1–M5 done (checkpoint #1 scope). Working on M6 (replay).

## Milestones

- [x] **M1 Docker controller** (2026-11-14) — `go test -tags integration ./pkg/docker`: starts 3 `kuru/fake-node`, health 200, kill+restart n2 (same host ports), pause/unpause n3, CopyLogs, `CleanupRun` leaves 0 containers/networks/volumes.
- [x] **M2 Proxy (http)** (2026-11-14) — unit tests (httptest, `-race`): sequential per-link IDs, body/headers intact, Drop → 504, Duplicate hits target twice, blocked link → `msg_drop reason=partition`, sender-gone still recorded, Close idempotent. Integration: 3 fake-nodes via proxy for 3 s pass-through → 382 messages, 0 sends without exactly one deliver.
- [x] **M3 Scheduler** (2026-11-14) — unit tests with a fake bus (`-race`): `Decide` identical across 20 runs for 900 IDs; identical ops across 20 shuffled arrival orders; delay released on the exact step; blocked → partition drop; `NoMessageFaults` delivers all; faults fire/heal sorted.
- [x] **M4 Fault plan + executor** (2026-11-14) — `Plan` identical for same seed, differs across seeds, `Validate` passes for seeds 1–100 at n = 1, 2, 3, 5, 7 (incl. dense configs that force no-overlap pushes and minority skips); toggling one fault type does not reshuffle others; executor tested with fake proxy/docker/HTTP (404 → `unsupported=true`, HealAll idempotent).
- [x] **M5 Engine + `kuru run`** (2026-11-14) — `kuru run examples/fake-smoke.yaml --seed 1` → `seed=1 verdict=PASS checks=fake-pass:PASS faults=9 dir=runs/fake-smoke/1`; trace has partitions (incl. bridge), crashes/restarts, pause/resume, clock skews, drops, dups, delays; 0 undecided messages. `scripts/engine-determinism.sh` → faults.json identical, 99.59% identical decisions on 488 shared IDs. Ctrl-C mid-run → verdict ERROR, exit 2, 0 containers/networks/volumes left. Integration tests: fake-smoke PASS + all files, cancel cleans up, missing image → ERROR result + cleanup.
- [ ] M6 Replay
- [ ] M7 Explorer + cleanup
- [ ] M8 Stream mode (tcp)
- [ ] M9 Hardening, performance, docs

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
- `scripts/engine-tracediff/` is a small Go command used by `scripts/engine-determinism.sh` (no jq/python dependency).

## Contract change requests

1. `pkg/core/core.go` is not gofmt-clean (alignment of the `FaultsFile` comment). Proposed: run `gofmt -w pkg/core/core.go` on main. Harmless; golangci-lint may flag it.
2. (Fixed on main already, commit "setup: fix fake-node Dockerfile to build a static binary") — `CGO_ENABLED=0` only applied to `go mod init`, so the image could not start.

## Known issues

- Message IDs count per link from the start of the run, including startup traffic, so the same ID can refer to different payloads in two runs if startup took a different number of messages. Decisions per ID are still identical; this only matters for replay fidelity.
- Messages parked after the last quiet step (between final reads and proxy close) get no decision event.

## Next step

M6: implement `pkg/replay` (guide from trace, next replay dir, fidelity), wire `prepareReplay`/`scoreReplay` in `pkg/engine/replay_hooks.go`, the replay admission/order logic in `pkg/scheduler/replay.go`, and `cmd/kuru/replay.go`.
