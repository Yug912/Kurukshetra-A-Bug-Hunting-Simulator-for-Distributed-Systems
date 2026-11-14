# Brain progress

## Status

M1–M2 done; M3 (checkers) next. Work happens on branch `anushka` (Anushka's own branch, standing in for `feat/brain`).

## Milestones

- [x] **M1 Toy Raft KV** (2026-11-14). `go test -race ./targets/toykv` passes: FSM, transport round trip (4 RPCs, duplicate request, heartbeat fast path, errors), in-process 3-node failover, lease clock logic. `docker build -t kuru/toykv -f targets/toykv/Dockerfile .` builds a 17 MB image. `go test -tags integration ./targets/toykv` passes (compose up, write/read, `docker kill` leader, new leader in 1.34 s, data still readable).
- [x] **M2 Workload `kv`** (2026-11-14). `go test -race ./pkg/workload` passes: well-formed history, op mix within ±8 %, deterministic per-client choices, ok/fail/info classification under chaos, rate limit (100 ops/s → 35–60 ops in 500 ms), cancel stops in < 300 ms with interrupted ops as `info`, final-read retries. `go test -tags integration ./pkg/workload` passes against compose: 10 s of traffic plus final reads, 1034 pairs, every ok read returns a written value or "".
- [ ] M3 Checkers
- [ ] M4 Timeline viewer v1
- [ ] M5 Shrinker
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

## Contract change requests

- None yet. (Note: `gofmt -l` reports `pkg/core/core.go` as not gofmt-clean with Go 1.26.11. It is a shared file, so I left it alone.)

## Known issues

- None.

## Next step

M3: implement `pkg/checker` (linearizable-kv with porcupine, durability) and `cmd/kuru/check.go`.
