# Kurukshetra engine

The engine runs one scenario with one seed on Docker. It puts a proxy between every pair of nodes, drives a logical clock, injects faults from a deterministic plan, runs the workload, checks the history and writes a run folder.

Contents: [Architecture](#architecture) · [Run phases](#run-phases) · [Proxy](#proxy) · [Scheduler](#scheduler) · [Faults](#faults) · [Determinism model](#determinism-model) · [Replay](#replay) · [Explore](#explore) · [Stream mode](#stream-mode-tcp) · [Run folder](#run-folder) · [CLI reference](#cli-reference) · [Troubleshooting](#troubleshooting) · [README snippet](#readme-snippet)

## Architecture

```
kuru run scenario.yaml --seed 42
  │
  └─ engine.Runner.Run                                       pkg/engine
       ├─ faults.Plan(seed, scenario) ──► faults.json         pkg/faults   (written before anything starts)
       ├─ docker.Controller ──► network, N containers,        pkg/docker
       │                         volumes, logs
       ├─ netproxy.Proxy ──► one listener per ordered pair    pkg/netproxy
       │                     i→j; parks every message
       ├─ scheduler.Scheduler (logical clock) ──► each step:  pkg/scheduler
       │     fire/heal faults, release delays, decide
       │     every parked message (rng.Keyed per message ID)
       ├─ faults.Executor ──► partitions (proxy),             pkg/faults
       │     crash/restart + pause/resume (Docker),
       │     clock skew (POST /kuru/clock)
       ├─ trace.Writer ──► trace.jsonl                         pkg/trace    (shared)
       ├─ history.FileRecorder ──► history.jsonl               pkg/history  (shared)
       ├─ core.NewWorkload(...).Run / FinalReads               Person B's workload, or fakes
       └─ core.CheckRun(dir, scenario.Checkers) ──► result.json  Person B's checkers, or fakes

             ┌──────── host (kuru process) ─────────┐
  node n1 ──►│ :p(n1→n2)  proxy  ──decide──►  n2 peer port │──► node n2
  (PEERS:    │ :p(n1→n3)    ▲                             │
   n2=host.docker.internal:p(n1→n2), ...)                 │
             │        scheduler (step clock)              │
             └─────────────────────────────────────────────┘
```

Nodes never talk to each other directly. Each node's `PEERS` points at the proxy listener for that ordered pair, so the proxy knows the sender and receiver from the port alone. It holds every request until the scheduler decides its fate. Clients (the workload) talk to each node's client port directly.

## Run phases

`engine.Runner.Run` (registered as runner `docker`) does, in order:

| Phase (logs/engine-timings.json) | What happens |
| --- | --- |
| (prepare) | Validate the scenario, recreate `runs/<scenario>/<seed>/`, write `scenario.yaml`, `meta.json`, and the fault plan to `faults.json` (or copy `--faults-from`). |
| `docker_setup` | Ping Docker, check the image exists locally (never pulls), create a labelled bridge network, open the proxy listeners. |
| `start_containers` | Start the N containers in parallel with `NODE_ID`, `PEER_LISTEN`, `CLIENT_LISTEN`, `PEERS` (proxy addresses), `DATA_DIR=/data`, `KURU_CLOCK_OFFSET_MS=0`, plus `scenario.env`. Each node gets a named volume at `/data` and two host ports on 127.0.0.1 that survive restarts. |
| `health` | Wait (with backoff, 30 s max) for `GET /kuru/health` = 200 on every node. Peer traffic passes straight through at step 0 with startup IDs (`<from>-<to>-s<n>`) so a Raft target can elect a leader. |
| `faults_and_workload` | The clock starts. The workload runs in a goroutine; the scheduler ticks `duration_steps` steps of `step_ms` each. |
| `heal_and_quiet` | `HealAll` (unblock links, restart crashed, unpause paused, reset clocks), then `quiet_steps` steps delivering everything. |
| `final_reads` | The workload reads every key from every node while the clock keeps ticking (quiet). `run_end` is written. |
| `teardown` | Wait for in-flight deliveries, close the proxy and writers, copy container logs to `logs/<node>.log`, remove containers, network and volumes (unless `--keep`). |
| `check` | `core.CheckRun(dir, scenario.checkers)` writes `result.json`; replays add `fidelity`. `meta.json` gets the duration and step count. |

Any error, including Ctrl-C, still runs teardown with a fresh context and writes `result.json` with verdict `ERROR` and the message.

## Proxy

`pkg/netproxy`. For each ordered pair (i → j) the proxy listens on `0.0.0.0:<random>` and forwards to node j's published peer port.

- **The proxy never decides.** It reads the request (body ≤ 64 MB), assigns the next ID on that link, writes `msg_send` (meta `path`, `mode=http`), parks the message and hands it to the scheduler. When told, it delivers (`msg_deliver`, forward, reply to the waiting sender), drops (`msg_drop`, the sender gets 504) or duplicates (`msg_dup` plus two `msg_deliver`; the target gets the request twice and the sender gets the first reply).
- A sender that gives up while its message is parked does not change anything: the decision is still taken and recorded.
- Delivery and duplicate events are written synchronously, in the scheduler's order. The network I/O runs in a goroutine, so a slow or paused node never stalls the clock.
- `SetBlocked` holds the set of partitioned directed links; `Blocked(from, to)` answers the scheduler.
- **Ports.** A port is used only if it is free on both 127.0.0.1 and 0.0.0.0, and the proxy tells the Docker controller which ports it holds. On macOS, a container port forward on `127.0.0.1:P` silently takes the loopback traffic meant for a proxy on `0.0.0.0:P`. The engine warns if a link carried no run-phase traffic at all.

## Scheduler

`pkg/scheduler`. The scheduler owns the logical clock (`scheduler.Clock`, shared with the proxy). Each tick (`step_ms` real milliseconds) it advances one step and, in this order:

1. Heals faults whose `step + duration_steps` is now, then fires faults whose `step` is now, each sorted by (step, kind, nodes, groups).
2. Releases delayed messages whose release step has come (no second draw).
3. Collects every message that arrived since the last step, sorted by ID (link, then number).
4. Decides each one. A blocked link means drop with `reason=partition`. Otherwise `Decide(seed, faults, id)` draws from `rng.Keyed(StreamMessages, id)`: drop with probability `drop`, else duplicate with `dup`, else delay `Between(delay_steps)` steps with `delay_prob` (writes `msg_delay` meta `steps`), else deliver. With `--no-message-faults`, and in the quiet period, everything is delivered (partitions still apply).
5. Delivers this step's messages in an order shuffled by `rng.Keyed(StreamMessages, "order-<step>")`. A delayed message released into a partition is dropped.

## Faults

`pkg/faults`.

**Plan** (`faults.Plan`, pure): for each enabled type, walk from step 0 with `next = prev + Between(every)` until `duration_steps`. Each type has its own generator, seeded in a fixed order (partition, crash, pause, clock_skew) from `rng.Stream(StreamFaults)`. The rules:

| Type | Rule |
| --- | --- |
| Partition | Strategy chosen uniformly from those that differ for this cluster size: `isolate-one` (one node vs the rest), `bridge` (n ≥ 3: two sides, plus one node in `nodes` that can talk to both), `majority-minority` (n ≥ 5: a random minority of floor((n−1)/2)). Length `Between(partition_len)`. Partitions never overlap: one that would is pushed to just after the previous heal. |
| Crash / pause | A random node, length `Between(crash_len)` / `Between(pause_len)`. Crashes and pauses share one budget: never more than floor((n−1)/2) nodes down at once, and never the same node twice. A fault that would break this is skipped. |
| Clock skew | A random node, `offset_ms = Between(clock_skew_ms)`, `duration_steps` 0: it lasts until the run heals everything. |

The plan is sorted by step, then kind. `faults.Validate` checks these rules; the engine warns when a `--faults-from` plan breaks them (the shrinker may hand it subsets).

**Executor**:

| Fault | Apply | Heal |
| --- | --- | --- |
| `partition` | Block every link between different groups (both directions), via the proxy. Trace: `partition` meta `groups` like `n1\|n2,n3`, plus `bridge` if set. | Unblock. Trace: `heal`. |
| `crash` | SIGKILL the container. Trace: `crash`. | Start the same container (same volume, same ports). Trace: `restart`. |
| `pause` | `docker pause`. Trace: `pause`. | `docker unpause`. Trace: `resume`. |
| `clock_skew` | `POST /kuru/clock {"offset_ms":X}`; a 404 is recorded as `unsupported=true`. Trace: `clock_skew` meta `offset_ms`. | Reset to 0 at `HealAll`. |

`HealAll` is idempotent. A fault that fails (e.g. clock skew on a node that is down) is recorded as a `note` event and the run continues.

## Determinism model

1. **The fault plan is a pure function of seed + scenario.** `faults.Plan` draws only from `rng.Stream(StreamFaults)`; each fault type gets its own sub-generator seeded from that stream in a fixed order (partition, crash, pause, clock_skew), so tuning one type never reshuffles another. `faults.json` is written before any container starts.
2. **Every message has a deterministic ID and a seed-driven fate.** IDs are `<from>-<to>-<n>`, where n counts run-phase messages on that link from 1. Traffic before the clock starts (boot, leader election, health checks) uses separate IDs `<from>-<to>-s<n>` and passes straight through. A message's fate (deliver, drop, duplicate, delay k steps) is `scheduler.Decide(seed, faults, id)`, which draws only from `rng.Keyed(StreamMessages, id)`, so arrival order never changes it. Deliveries within a step happen in an order shuffled by `rng.Keyed(StreamMessages, "order-<step>")`.
3. **Replay also forces the recorded fate, timing and order** (see Replay below).

What is **not** controlled: thread scheduling and timers inside each node. A node decides *when* it sends from its own wall clock, so the step at which message n arrives can differ by a few steps between two runs. Two consequences:

- A message that arrives near a partition boundary may be inside the partition in one run and outside it in the other (`drop:partition` vs `deliver`). The same goes for the boundary of the quiet period, where everything is delivered.
- So "same seed, same decisions" holds exactly for **seed-driven decisions**, and approximately for all decisions.

`scripts/engine-determinism.sh` measures both. Typical result on `examples/fake-smoke.yaml`: fault plan 100% identical, seed-driven decisions 100% identical, all decisions 95–100% identical (the rest sit on partition/quiet boundaries).

## Replay

`kuru replay <run-dir>` re-runs a run with the same seed, scenario and `faults.json`, writes `<run-dir>-replay-<n>`, and forces what was recorded:

- **Matching.** In a replay the proxy does not number messages with a plain counter. Each arriving message on a link claims the next unclaimed *recorded* message of that link, by timing:
  - a message may claim a recorded message up to 50 steps early (it is then held until the recorded step) or 30 steps late;
  - a recorded message that is late, when the one after it is nearer, is skipped as missed;
  - for the first message of a link, one that arrives more than half a gap before the first recorded message is the counterpart of the original's startup traffic: it gets an `-sx<k>` ID and is delivered immediately, as it was then;
  - anything else is an extra (`-x<k>` ID) and gets the PRNG decision.

  This keeps the two runs aligned across the startup boundary, restarts and timer drift. A fixed counter would slip by one message whenever a node sent one more or one fewer message than before.
- **Decisions.** A matched message gets its recorded fate (including the delay length and drop reason). Partitions still apply: a link that is cut drops.
- **Timing.** A matched message is decided at its recorded step: it is held if it arrived early, and the clock waits for it if it is late. The clock pauses (up to 30 ticks per step) while a message recorded for the next step is still the next one expected on its link. It never waits for a sender that has a message held by the proxy, because nodes usually send synchronously and that sender is blocked until the clock moves. Each pause moves the clock behind the nodes' timers, so later messages arrive early and are held, and the clock phase-locks to the nodes.
- **Order.** Deliveries within a step follow the recorded order.

**Fidelity** = (recorded message decisions reproduced with the same fate within ±2 steps + fault events reproduced exactly) / (recorded message decisions + fault events). `kuru replay` prints the breakdown (matched, shifted, changed, missed, extra; faults) and whether the verdict was reproduced. The same numbers, plus the scheduler's own counters, are in `logs/replay-fidelity.json`.

### Why fidelity can be below 100%

- **Node-internal timers.** Heartbeats, election timeouts and retries fire on the node's wall clock. Replay can delay a message (hold it) or stop the clock (wait for it), but it cannot make a node send earlier. A node whose timer phase jumped (after a restart or a pause) can be late by more than the 30-tick wait, and a few of its messages are then decided a few steps late ("shifted").
- **Goroutine scheduling inside nodes.** The order in which a node handles concurrent requests, and so what it sends next, is up to the Go runtime (or the target's runtime).
- **Content drift.** A message the replay matched by link and timing may carry a different payload if the node's state already diverged. Fidelity counts decisions, not payloads.

Measured on `examples/fake-smoke.yaml`, seeds 1–5, eight rounds (40 run+replay pairs): every replay ≥ 95% (lowest 96.2%, 35 of 40 at 99.6–100%), fault events 100% every time.

### What would make it 100%

Running the nodes in-process, with their timers, randomness and goroutine scheduling driven by the engine's logical clock (a Go SDK in the spirit of deterministic simulation), would turn replay into exact re-execution. That is a stretch goal; the Docker engine reports fidelity instead of claiming perfection.

## Explore

`kuru explore` runs seeds S..S+N−1 with P runners in parallel. Every run has its own Docker network, container names and ports, so runs never interfere.

- Passing run folders are deleted unless `--keep-passing`; FAIL and ERROR are always kept.
- Every finished run is appended to `runs/<scenario>/summary.csv` (`seed,verdict,checks,faults,steps,duration_ms,dir`) by a single writer. Re-running appends; the header is written once.
- A live line every second: `done 120/500 fail 3 error 0 rate 41/min eta 9m`.
- `--stop-after-fails K` stops handing out seeds after exactly K FAILs.
- Ctrl-C stops handing out seeds; the runs in flight clean up and are reported as interrupted, not as results.
- At the end it prints the failing seeds and the `kuru shrink` / `kuru replay` commands to try.
- Throughput on a laptop (Colima, 4 CPUs): about 40–45 fake-smoke runs per minute with `--parallel 4`; the fake runners do hundreds of thousands per minute.

## Stream mode (tcp)

With `mode: tcp` the proxy carries raw TCP instead of HTTP requests, for black-box targets.

- Per ordered pair (i → j) it accepts connections and pipes them to node j. Every `Read` chunk from the client side is a message `<from>-<to>-<n>` (meta `mode=tcp`, `conn=<k>`) decided by the same scheduler.
- Allowed fates: deliver; delay (chunks of a connection are written in order, so a delayed chunk also holds back later chunks of that connection); drop, which resets the connection (`msg_drop` meta `reason=reset`). Duplicate is treated as deliver.
- Partitioned links refuse new connections and reset existing ones (recorded as `note` events with `reason=partition`).
- The reply direction is piped directly but recorded as `msg_send`/`msg_deliver` with meta `dir=reply` (IDs `<to>-<from>-r<conn>.<n>`), so the UI can show it. Replay and the determinism diff ignore these.

`examples/engine-tcp-smoke.yaml` runs the fake node in tcp mode: its peer HTTP traffic is carried as TCP chunks.

## Run folder

`runs/<scenario>/<seed>/` (replays: `runs/<scenario>/<seed>-replay-<n>/`):

| File | Content |
| --- | --- |
| `scenario.yaml` | The scenario as run (defaults applied). |
| `meta.json` | Seed, nodes, mode, runner, start time, duration, steps, `replay_of`, `faults_from`, version. |
| `faults.json` | The fault plan actually used. |
| `trace.jsonl` | Every event: `run_start`, message events, fault events, `note`, `run_end`. |
| `history.jsonl` | Every client operation (from the workload). |
| `result.json` | Verdict, per-checker results, `fidelity` for replays. |
| `logs/<node>.log` | Container stdout/stderr with timestamps. |
| `logs/engine.log` | Engine phases and every non-fatal error (failed deliveries to crashed nodes, failed faults). |
| `logs/engine-timings.json` | Milliseconds per phase. |
| `logs/replay-fidelity.json` | Replays only: fidelity breakdown and the replay scheduler's counters. |

## CLI reference

Global flags: `--runs-dir runs` (output root), `--runner docker` (also `fake-sample`, `fake-faults`), `-v`.

| Command | What it does | Exit code |
| --- | --- | --- |
| `kuru run <scenario.yaml> [--seed N] [--keep] [--faults-from F] [--no-message-faults] [-q]` | One run. Without `--seed` a random seed is chosen and printed. Prints `seed=42 verdict=FAIL checks=linearizable-kv:FAIL faults=7 dir=runs/x/42`. | 0 PASS, 1 FAIL, 2 ERROR/UNKNOWN |
| `kuru replay <run-dir> [--keep] [-q]` | Replay a run; prints the fidelity breakdown and whether the verdict was reproduced. | of the replay's verdict |
| `kuru explore <scenario.yaml> --runs N [--parallel 4] [--start-seed 1] [--keep-passing] [--stop-after-fails K] [--no-message-faults] [-q]` | Many seeds in parallel; `summary.csv`. | 0 none failing, 1 any FAIL, 2 errors only |
| `kuru cleanup` | Remove every container, network and volume labelled `kuru=1`; lists what it removed. | 0 |

Examples:

```sh
make images                                              # build kuru/fake-node (and kuru/toykv)
kuru run examples/fake-smoke.yaml --seed 1               # one Docker run
kuru replay runs/fake-smoke/1                            # replay it, print fidelity
kuru explore examples/fake-smoke.yaml --runs 50 --parallel 4
kuru explore examples/fake-faults.yaml --runner fake-faults --runs 500 --parallel 8   # no Docker
kuru run examples/fake-smoke.yaml --seed 1 --faults-from runs/fake-smoke/1/faults.json --no-message-faults
kuru cleanup
scripts/engine-determinism.sh examples/fake-smoke.yaml 1  # same seed twice: plan + decisions
scripts/engine-replay-check.sh examples/fake-smoke.yaml 1 5  # run+replay seeds 1..5, fidelity ≥ 95%
```

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `docker ping (is Docker running?)` | Start Docker Desktop, or `colima start`. With Colima, kuru finds `~/.colima/default/docker.sock` by itself if `DOCKER_HOST` is unset; Docker Desktop, OrbStack and Rancher Desktop sockets are tried too. |
| `image kuru/fake-node not found locally (run make images)` | kuru never pulls. Build the images with `make images`. |
| Nodes never become healthy; `logs/<node>.log` shows peer timeouts | Containers cannot reach the proxy on the host. Docker Desktop and Colima resolve `host.docker.internal`; on Linux, kuru adds `host.docker.internal:host-gateway` (Docker ≥ 20.10). If your setup differs, set `KURU_PROXY_HOST` to an address of the host that containers can reach (e.g. the `docker0` IP `172.17.0.1`). A host firewall must allow the proxy's random ports from the Docker bridge. |
| Warning `no run-phase messages on links …` | Peer traffic on those links bypassed the proxy or the target never uses them. Check the target reads `PEERS` and does not discover peers another way. |
| `node X exited with code N before publishing ports` | The image crashed at start; the message includes its last log lines. |
| Many `warn: deliver …: connection refused` in `logs/engine.log` | Expected while a node is crashed or paused; the console only shows a count. |
| Leftover containers after a crash of kuru itself | `kuru cleanup` (everything labelled `kuru=1`). `--keep` runs also stay until then. |
| Runs are slow | Lower `step_ms` (default 2) or `duration_steps`; use `--parallel`; check `logs/engine-timings.json` for the slow phase. |

## README snippet

> Paste into README.md at the final merge (README is shared).

```markdown
## The engine

Kurukshetra runs your cluster (3–5 Docker containers) behind a proxy that owns every node-to-node message. From one seed it decides which messages are delivered, dropped, delayed or duplicated, and when partitions, crashes, pauses and clock skews happen. Everything goes into a trace that can be checked, replayed and visualized.

    make images
    kuru run examples/fake-smoke.yaml --seed 42      # one run → runs/fake-smoke/42/
    kuru replay runs/fake-smoke/42                    # re-run it, forcing the same decisions
    kuru explore examples/fake-smoke.yaml --runs 200  # many seeds in parallel, keep the failures
    kuru cleanup                                      # remove anything kuru left behind

Same seed, same fault plan, same message decisions. Replay reports a fidelity score because node-internal timers are not controlled. See [docs/engine.md](docs/engine.md) for the architecture, determinism model, CLI and troubleshooting.
```
