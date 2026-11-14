# Kurukshetra

An open-source bug-hunting simulator for distributed systems. Kurukshetra runs a small cluster (3–5 Docker containers) under a controller that owns the network between nodes, injects faults (dropped, delayed or duplicated messages, partitions, crashes, pauses, clock skew) chosen from one random seed, and records everything into a trace. It then checks the client history for consistency bugs (linearizability, lost writes), shrinks failing runs, replays them and shows them on a React timeline.

## How the three prompts work

1. **Prompt 0 (shared setup)** ran once on `main`. It wrote the shared contracts (`pkg/trace`, `pkg/history`, `pkg/scenario`, `pkg/rng`, `pkg/core`), the node protocol (`docs/PROTOCOL.md`), fakes, a sample run, and created the two branches.
2. **Prompt A (engine)** runs on `feat/engine` (Person A).
3. **Prompt B (checkers, target, UI)** runs on `feat/brain` (Person B).

Both branches code against the contracts on `main` and merge back at checkpoints; see [CONTRIBUTING.md](CONTRIBUTING.md).

## Make targets

| Target | What it does |
| --- | --- |
| `make build` | Builds `bin/kuru` |
| `make test` | `go test ./...` |
| `make test-integration` | Tests tagged `integration` (need Docker) |
| `make lint` | golangci-lint if installed, else `go vet` |
| `make images` | Builds `kuru/fake-node` (and `kuru/toykv` once it exists) |
| `make sample` | Regenerates `testdata/sample-run/` |
| `make web` | Builds the timeline UI once `web/timeline/package.json` exists |
| `make clean` | Removes build outputs and `runs/` |

## Try it without Docker

```sh
go run ./cmd/kuru --help
go test ./...
```

The `fake-faults` and `fake-sample` runners (in `pkg/fakes`) produce run folders without Docker; Person A's `kuru run` command will expose them via `--runner`.
