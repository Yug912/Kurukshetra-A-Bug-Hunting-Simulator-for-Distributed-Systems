# Setup notes

Decisions and deviations made while running Prompt 0 (shared setup) on `main`.

## Environment

- Built and tested with Go 1.27.4 (Homebrew) on macOS arm64.
- Docker was not installed on the setup machine, so `kuru/fake-node` was **not built as an image**. Instead, three `fake-node` processes were run locally with real `PEERS` wiring, and `/kuru/health`, `PUT`/`GET /kv`, `404`, CAS `409`/`200`, `/kuru/clock` and `/peer/*` echo were all checked by hand. Run `make images` on a machine with Docker before the engine's first integration run.

## Deviations

1. **Go floor is 1.26, not 1.22.** `go.mod` says `go 1.26.0`: the Docker client's transitive dependencies (OpenTelemetry, `golang.org/x/sys`) require it, and `go get` raises the directive automatically. CI uses `go-version-file: go.mod` and the fake-node Dockerfile uses `golang:1.26`. Everyone needs Go 1.26+.
2. **Docker client module.** The classic import path `github.com/docker/docker/client` is kept, from `github.com/docker/docker v28.5.2+incompatible`. `go get github.com/docker/docker/client@latest` now resolves to a module that declares itself `github.com/moby/moby/client`; that newer module is not used.
3. **`github.com/hashicorp/raft-boltdb/v2`** resolved (v2.4.2); no fallback was needed.
4. **Run folder layout.** The spec's comment for the layout was garbled in the source doc; `RunDir(root, scenario, seed)` returns `<root>/<scenario>/<seed>` (e.g. `runs/fake-faults/1`).
5. **fake-faults plan generation.** To make seed 1 contain both a partition isolating `n1` and a crash of `n3`, the 8-fault plan cycles through the four kinds in order (partition, crash, pause, clock_skew; each appears twice), and each kind walks its own shuffled node order drawn from `rng.New(seed).Keyed(StreamFaults, "nodes/<kind>")`. Steps, durations and offsets come from `Stream(StreamFaults)`. Seed 1 FAILs (tested); other seeds vary.
6. **Small helpers beyond the spec** (no new behaviour, only conveniences both sides will want): `core.VerdictPass/Fail/Error/Unknown` constants, `core.Version`, `core.SortFaults`, `scenario.Range.Enabled`, `fakes.GeneratePlan`, `fakes.PlanFails`, `fakes.PlanSize`, `fakes.SampleDir`, and `verbose()` in `cmd/kuru/root.go`.
7. **Scenario parsing is strict.** `scenario.Parse` rejects unknown YAML fields, so typos in scenario files fail loudly.
8. **Workload fraction defaults** (0.4/0.4/0.2) are applied only when all three fractions are zero; if any is set, all three are taken as written and must sum to 1.
9. **`history.Pairs`** also returns an error for a completion with no outstanding invoke and for unknown op types.
10. **golangci-lint** config uses the v2 format (`version: "2"`, standard linters), matching `golangci-lint-action@v8`.
11. **Sample meta version** is the fixed string `"sample"` so bumping `core.Version` never invalidates the committed `testdata/sample-run/`. `cmd/gen-sample`'s test regenerates the sample and fails if it differs from the committed copy (fix with `make sample`).
12. **fake-faults trace/history** are empty files (zero events), which `trace.ReadFile` and `history.ReadFile` accept.
