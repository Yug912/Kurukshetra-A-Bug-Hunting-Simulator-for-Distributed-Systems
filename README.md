# Kurukshetra

An open-source bug-hunting simulator for distributed systems. Kurukshetra runs a small cluster (3–5 Docker containers) under a controller that owns the network between nodes, injects faults (dropped, delayed or duplicated messages, partitions, crashes, pauses, clock skew) chosen from one random seed, and records everything into a trace. It then checks the client history for consistency bugs (linearizability, lost writes), shrinks failing runs, replays them and shows them on a React timeline.

## Quick start

Needs Go 1.26, Docker (Docker Desktop, Colima or Linux) and Node 20 for the viewer.

```sh
make images                                              # kuru/fake-node, kuru/toykv
go run ./cmd/kuru run examples/toykv-stale-read.yaml --seed 1    # → FAIL: stale read found
go run ./cmd/kuru shrink runs/toykv-stale-read/1         # minimal failing fault schedule
go run ./cmd/kuru replay runs/toykv-stale-read/1         # re-run with the recorded decisions
make web && go run ./cmd/kuru serve --open               # timeline viewer
```

## The engine

Kurukshetra runs your cluster (3–5 Docker containers) behind a proxy that owns every node-to-node message. From one seed it decides which messages are delivered, dropped, delayed or duplicated, and when partitions, crashes, pauses and clock skews happen. Everything goes into a trace that can be checked, replayed and visualized.

```sh
kuru run examples/fake-smoke.yaml --seed 42      # one run → runs/fake-smoke/42/
kuru replay runs/fake-smoke/42                    # re-run it, forcing the same decisions
kuru explore examples/fake-smoke.yaml --runs 200  # many seeds in parallel, keep the failures
kuru cleanup                                      # remove anything kuru left behind
```

Same seed, same fault plan, same message decisions. Replay reports a fidelity score because node-internal timers are not controlled. Scenarios with `mode: tcp` proxy raw TCP streams for real-world targets. See [docs/engine.md](docs/engine.md) for the architecture, determinism model, CLI and troubleshooting.

## Finding bugs

- `kuru check <run-dir> [--checkers linearizable-kv,durability]` re-judges a run (exit 0 PASS, 1 FAIL, 2 other).
- `kuru shrink <run-dir>` finds a minimal failing fault schedule → `minimal.json`, `shrink.log`, `<run-dir>-min`.
- `make web && kuru serve --open` opens the timeline viewer: run list, player, jump to the first violation, compare a run with its replay or shrink.
- Targets: `targets/toykv` (Raft KV with `BUG_STALE_READ`, `BUG_ACK_BEFORE_COMMIT`, `BUG_LEASE_READ`) and `targets/etcd`
  (etcd 2.3.8's stale reads vs 3.5.17). Build images with `scripts/brain-build-images.sh`.
- Details: [docs/brain.md](docs/brain.md); stage demo: [docs/demo.md](docs/demo.md).

## Make targets

| Target | What it does |
| --- | --- |
| `make build` | Builds `bin/kuru` |
| `make test` | `go test ./...` |
| `make test-integration` | Tests tagged `integration` (need Docker and `docker compose`) |
| `make lint` | golangci-lint if installed, else `go vet` |
| `make images` | Builds `kuru/fake-node` and `kuru/toykv` |
| `make sample` | Regenerates `testdata/sample-run/` |
| `make web` | Builds the timeline UI (`web/timeline`) |
| `make clean` | Removes build outputs and `runs/` |

## Try it without Docker

```sh
go run ./cmd/kuru --help
go test ./...
go run ./cmd/kuru explore examples/fake-faults.yaml --runner fake-faults --runs 500 --parallel 8
```

The `fake-faults` and `fake-sample` runners (in `pkg/fakes`) produce run folders without Docker.

## How it was built

1. **Shared setup** on `main`: the contracts (`pkg/trace`, `pkg/history`, `pkg/scenario`, `pkg/rng`, `pkg/core`), the node protocol ([docs/PROTOCOL.md](docs/PROTOCOL.md)), fakes and a sample run.
2. **Engine** (Person A, `feat/engine`): Docker controller, proxy, scheduler, faults, replay, explorer. Progress: [docs/progress-engine.md](docs/progress-engine.md).
3. **Brain** (Person B, `anushka`): toy Raft KV, workloads, checkers, shrinker, `kuru serve`, timeline viewer, etcd target. Progress: [docs/progress-brain.md](docs/progress-brain.md).

See [CONTRIBUTING.md](CONTRIBUTING.md).
