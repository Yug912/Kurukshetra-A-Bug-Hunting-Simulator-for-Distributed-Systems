# Contributing to Kurukshetra

Two people build Kurukshetra in parallel on top of the shared contracts on `main`:

| Role | Person | Branch | Builds |
| --- | --- | --- | --- |
| Person A | Yug | `feat/engine` | Engine: Docker, network proxy, scheduler, faults, replay, explore |
| Person B | Anushka | `feat/brain` | Workload, checkers, shrinker, toy Raft KV target, timeline UI |

## Ownership

| Owner | Paths |
| --- | --- |
| Shared (change only by a PR to `main` approved by both) | `go.mod`, `go.sum`, `internal/`, `pkg/trace`, `pkg/history`, `pkg/scenario`, `pkg/rng`, `pkg/core`, `pkg/fakes`, `cmd/kuru/root.go`, `cmd/kuru/imports.go`, `cmd/kuru/version.go`, `cmd/fake-node`, `cmd/gen-sample`, `testdata/`, `docs/PROTOCOL.md`, `examples/fake-*`, `Makefile`, `.github/` |
| Person A (`feat/engine`) | `pkg/docker`, `pkg/netproxy`, `pkg/scheduler`, `pkg/faults`, `pkg/engine`, `pkg/replay`, `pkg/explore`, `cmd/kuru/{run,replay,explore,cleanup}.go`, `docs/engine.md`, `docs/progress-engine.md`, `scripts/engine-*` |
| Person B (`feat/brain`) | `pkg/workload`, `pkg/checker`, `pkg/shrink`, `targets/`, `web/`, `cmd/kuru/{check,shrink,serve}.go`, `examples/toykv-*`, `examples/real-*`, `docs/brain.md`, `docs/demo.md`, `docs/progress-brain.md`, `scripts/brain-*` |

## Git rules

- Branch off `main`; push daily.
- Rebase on `origin/main` twice a week.
- Never edit the other person's paths.
- Contract changes (shared paths) go in a separate small PR to `main`.
- Merge order at each checkpoint: `feat/engine` first, then `feat/brain` rebases and merges.

## Checkpoints

1. Checkpoint #1: end of week 2.
2. Checkpoint #2: end of week 3.
3. Final: day 25, then tag `v0.1.0`.
