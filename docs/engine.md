# Kurukshetra engine

The engine runs one scenario with one seed on Docker. It puts a proxy between every pair of nodes, drives a logical clock, injects faults from a deterministic plan, runs the workload, checks the history and writes a run folder.

<!-- Sections below are filled in as milestones land; M9 completes this document. -->

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
