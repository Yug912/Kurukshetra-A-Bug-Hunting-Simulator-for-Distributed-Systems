# Brain: target, workload, checkers, shrinker, viewer

The brain side decides whether a run misbehaved and shows why. This guide covers what it contains, how each part behaves, and where each part stops. Progress, decisions and evidence are tracked in [progress-brain.md](progress-brain.md).

| Part | Where | One line |
| --- | --- | --- |
| Toy Raft KV | `targets/toykv` | hashicorp/raft KV over an HTTP transport (one request per RPC), with 3 bug flags |
| Real-world target | `targets/etcd` | etcd 2.3.8 (stale reads) vs 3.5.17 (fixed) behind `kuru-etcd-shim` |
| Workloads | `pkg/workload` | `kv` (PROTOCOL.md) and `etcd-kv` (etcd v2/v3 API) record `history.jsonl` |
| Checkers | `pkg/checker` | `linearizable-kv` (porcupine) and `durability` |
| Shrinker | `pkg/shrink`, `kuru shrink` | messages-off → ddmin over faults → halve durations |
| Viewer | `web/timeline`, `kuru serve` | React/SVG timeline with player, run list, compare mode |

## Toy KV

A small Go program on `hashicorp/raft` that follows [PROTOCOL.md](PROTOCOL.md). Full details are in [targets/toykv/README.md](../targets/toykv/README.md).

- **Transport.** `raft.Transport` is implemented over HTTP: `POST /peer/{append_entries,request_vote,install_snapshot,timeout_now}`, one request per RPC. Server addresses are node IDs mapped through `PEERS`, so the engine's proxy sees and controls every message. Timeouts are 500 ms for RPCs and 5 s for snapshots.
- **Correct mode.** Only the leader serves reads, and it sends each read through the Raft log. Writes wait for the commit (2 s). Responses: `421` not leader, `503` no leader or not enqueued; both mean the op was definitely not applied. `500`/`504` mean the outcome is unknown, i.e. the entry may still commit.
- **Clock.** `POST /kuru/clock` moves only toy KV's own logical clock (`now() = time.Now() + offset`). hashicorp/raft's timers cannot be skewed, so the offset matters only to the lease bug.

| Bug flag | What the node does wrong | What the checkers report |
| --- | --- | --- |
| `BUG_STALE_READ=1` | Every node answers `GET` from its local state | linearizable-kv FAIL: a partitioned or lagging node returns an old value |
| `BUG_ACK_BEFORE_COMMIT=1` | The leader answers 200 to `PUT`/`CAS` before the entry commits | durability FAIL (acknowledged write lost) and linearizable-kv FAIL, when the leader is cut off or crashes before replicating |
| `BUG_LEASE_READ=1` | The leader reads locally while `now() < lastAck + 300 ms`, on the skewable clock | linearizable-kv FAIL when a backwards clock skew plus a partition let the old leader serve reads after a new leader exists |

Engine-free proof: `go test -tags integration -run ToyKVBugFlags ./pkg/workload` cuts the leader off from its peers for 6 s. The correct build PASSes and each flag FAILs. Measured results are in [progress-brain.md](progress-brain.md).

## Workload semantics

`kv` and `etcd-kv` are the same register workload with different wire protocols.

- **Clients.** `clients` goroutines. Each gets its own generator, drawn from the run's workload stream in client order, so op choices are deterministic per seed. Each client has at most one op outstanding.
- **Ops.** Read, write or CAS by `read_frac`/`write_frac`/`cas_frac`; a uniformly chosen node; keys `k0..k{keys-1}`. Values are unique per client: `c<client>-<n>`. A CAS expects the last value this client saw for the key (`""` = missing).
- **Rate.** One shared limiter spaces ops evenly to `ops_per_sec` in total.
- **Recording.** `invoke` before sending, then the completion:

| Completion | Meaning | `kv` (PROTOCOL.md) | `etcd-kv` |
| --- | --- | --- | --- |
| `ok` | it happened (reads carry the value) | 200; 404 on read = `""` | 200/201; v2 404 code 100 = `""`; v3 empty range = `""` |
| `fail` | it definitely did not happen | 409, 421, 503 | CAS mismatch (v2 412/404, v3 `succeeded:false`), other 4xx |
| `info` | unknown: it may or may not have happened | timeout, connection error, any other status | timeout, 5xx |

- **Cancellation.** When the run ends, an in-flight op completes as `info`.
- **Final reads.** After healing, every key is read from every node (`final: true`, its own client id = `clients`). A `fail` is retried up to 5 times over 2 s so the leader's view gets recorded.

## Checkers

### linearizable-kv

- **Model.** [porcupine](https://github.com/anishathalye/porcupine) with a nondeterministic register model, partitioned by key. The initial state is `""` (missing).
- **Rules.**
  - A `fail` op is dropped.
  - An `info` (or never-completed) write or CAS gets a return time at the end of the history and may either take effect or not.
  - An `info` read is dropped, since it constrains nothing.
  - A read is legal iff it returns the current value. A CAS is legal iff the current value equals `expect`.
- **Verdict.** `Ok` → PASS, `Illegal` → FAIL, timeout (10 s) → UNKNOWN "timeout".
- **On FAIL.**
  - The culprit is found from the longest linearizable prefix of the failing key: of the ops left out, it is the one that has to return first. It is blamed together with every un-linearized op that overlaps it in time.
  - `bad_ops` lists the invoke and completion indices.
  - The reason reads like `stale read of k0 on n1: got c0-1, expected c1-1`, where "expected" is the state after that prefix.
  - `linearizability.html` (porcupine's visualization) is written to the run dir.
- **Limits.**
  - The time order comes from the kuru process's clock (`wall_ns`), so ops that only nearly overlap are treated as concurrent. This is safe: it can hide a violation but never invent one.
  - Very long histories with many `info` ops can hit the 10 s budget (UNKNOWN).

### durability

An acknowledged (`ok`) write or CAS W is **lost** when all three hold:

1. No ok read ever returned W's value. Values are unique, so any such read proves W happened.
2. No other write or CAS on the key could be ordered after W, meaning none returned (or, being `info`, might still return) after W was invoked.
3. The key has at least one ok final read, and none returned W's value.

Any lost write → FAIL with `bad_ops` = W's indices and the reason `acknowledged write k1=c2-7 lost`. No ok final reads at all → UNKNOWN. A key without an ok final read is not judged.

**Limit:** a lost write that a later write overwrites is invisible to `durability`. `linearizable-kv` still catches it whenever a read observes the gap.

## Shrinker

`kuru shrink <run-dir> [--runner R] [--max-runs 60] [--parallel 1]` works only on FAIL runs. Each trial re-runs the scenario with the same seed and `RunOptions.FaultsFrom`; "still failing" means the verdict is FAIL from any checker.

1. **Messages off.** Re-run all faults with `NoMessageFaults`. If it still fails, message faults stay off for every later trial.
2. **ddmin.** Zeller's delta debugging over the fault list. Each round's candidates run as one batch (up to `--parallel`), and the first failing candidate in order wins, so the result is deterministic. Results are cached by the canonical JSON of the faults, and cache hits don't count against `--max-runs`.
3. **Shorten.** Halve each remaining fault's `duration_steps` while it still fails.

**Output.** `minimal.json` and `shrink.log` (every trial with its verdict) go into the run dir, and `<run-dir>-min` is the trial run that proved the minimal schedule fails (`meta.faults_from` → minimal.json). It prints, e.g., `minimal: 2 faults (from 8) — crash n3 at step 195 for 1; partition n1 | n2,n3 at step 420 for 1 — 36 runs`. The runner is `--runner` if given, else the one in `meta.json` if registered, else `docker`.

## Viewer

Build it with `make web`, then run `kuru serve [--addr 127.0.0.1:7777] [--web-dir web/timeline/dist] [--open]` and open http://127.0.0.1:7777/. Without a backend, `cd web/timeline && npm run dev` serves `?run=sample` (the bundled sample) and `?run=synthetic` (50,000 generated events).

- **Run list** (home page): every run with scenario/verdict filters, plus "vs original" (replay or `-min`) and "vs minimal" compare links.
- **Header:** scenario, seed, verdict badge, fidelity, fault count, each checker's verdict and reason, and links to raw files and `linearizability.html`.
- **Node lanes** (n1 at the top):
  - Messages are arrows from send to delivery. Drops end in a red ✕, duplicates are dashed, delayed messages are amber.
  - A partition is a shaded band labelled with its groups. A crash is a hatched red block, a pause a grey bar, and a clock skew an amber `⏱±N ms` badge.
- **Client lanes:** one bar per op from invoke to completion. Green = ok, grey = fail, amber = info. A thick red outline marks an op in `bad_ops`; final reads are dashed.
- **Details panel:** a friendly description plus the raw JSON of the clicked item, and the **cluster state at the playhead** (each node up/down/paused, partition groups, clock offsets, the leader from trace notes).
- **Player:** playhead, play/pause, ±1 / ±100 steps, speed 1×–50× (1× = 100 steps/s). Events after the playhead are dimmed. **⚑ jump to first violation** moves to the first bad op and centres the view.
- **Compare:** `?run=A&compare=B` stacks two runs with a shared playhead and viewport.
- **Zoom and pan:** the mouse wheel zooms around the cursor and dragging pans. The minimap shows the whole run: click or drag it to move. Only the visible step range is drawn; past 1,500 ordinary messages it thins them out evenly and says so.
- **URL** keeps `run`, `compare`, `step`, `from`/`to` (viewport) and `filters` (hidden nodes, kinds, clients, paths), so a link reopens the same view.

| Key | Action |
| --- | --- |
| space | play / pause |
| ← / → | step −1 / +1 |
| shift + ← / → | step −100 / +100 |

## kuru serve API

All endpoints are read-only `GET`s. CORS allows `http://localhost:5173` and `http://127.0.0.1:5173` (the Vite dev server, which also proxies `/api`).

| Endpoint | Returns |
| --- | --- |
| `/api/runs` | `[{id, scenario, seed, verdict, fidelity, faults, started_at, replay_of}]`, newest first. A run is any `<runs>/<scenario>/<run>` folder with `meta.json` or `result.json`; `id` = `<scenario>/<run>` |
| `/api/runs/{scenario}/{run}/{file}` | One of `trace.jsonl`, `history.jsonl`, `result.json`, `faults.json`, `meta.json`, `scenario.yaml`, `minimal.json`, `shrink.log`, `linearizability.html`, `logs/<node>.log`. Anything else, `..`, absolute paths and symlinks out of the runs dir → 404 |
| `/api/summary/{scenario}` | `<runs>/<scenario>/summary.csv` (the explorer's) as JSON objects keyed by the header row |
| everything else | the built viewer, with SPA fallback to `index.html`. If `--web-dir` is missing, a 503 page says to run `make web` |

## README snippet

*For the humans to paste into `README.md` at the final merge.*

```markdown
### Finding bugs (brain)

- `kuru check <run-dir> [--checkers linearizable-kv,durability]` re-judges a run (exit 0 PASS, 1 FAIL, 2 other).
- `kuru shrink <run-dir>` finds a minimal failing fault schedule → `minimal.json`, `shrink.log`, `<run-dir>-min`.
- `make web && kuru serve --open` opens the timeline viewer: run list, player, jump to the first violation, compare a run with its replay or shrink.
- Targets: `targets/toykv` (Raft KV with `BUG_STALE_READ`, `BUG_ACK_BEFORE_COMMIT`, `BUG_LEASE_READ`) and `targets/etcd`
  (etcd 2.3.8's stale reads vs 3.5.17). Build images with `scripts/brain-build-images.sh`.
- Details: [docs/brain.md](docs/brain.md); stage demo: [docs/demo.md](docs/demo.md).
```
