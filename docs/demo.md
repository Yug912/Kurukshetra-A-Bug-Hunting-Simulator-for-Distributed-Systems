# Demo: 4 minutes on stage

**Story:** Kurukshetra finds a stale read in a Raft KV store, shrinks it (this bug needs no faults at all), shows it on a timeline, explains it in plain English, replays it, then catches a clock-skew bug and a real etcd bug, while the correct build passes 200 seeds.

Rehearsed 2026-11-20 against `scripts/brain-demo-prep.sh` output (24 min on a MacBook Air with Colima): every "Expect" below matches what the prepped runs show.

## Before the talk (the day before)

```sh
git checkout main && git pull
scripts/brain-demo-prep.sh            # builds kuru, the viewer and all images; records every run below in runs/
./bin/kuru serve --open               # check the run list shows everything, then leave it running
```

`brain-demo-prep.sh` writes the path of the first stale-read FAIL to `runs/.demo-stale-read`. Below, `$FAIL` means that path (`FAIL=$(cat runs/.demo-stale-read)`). Have two terminals and the browser ready, with the font size up.

## Script

| Time | Say | Do | Expect |
| --- | --- | --- | --- |
| 0:00 | "Three Raft nodes in Docker. Kurukshetra owns every message between them and injects faults from one seed." | `cat examples/toykv-stale-read.yaml` | `BUG_STALE_READ: "1"`, partitions, crashes, drops, delays |
| 0:20 | "Let's explore 50 seeds." | `./bin/kuru explore examples/toykv-stale-read.yaml --runs 50 --parallel 4` | a table of seeds; some FAIL with `stale read of kX on nY: got …, expected …` |
| 0:50 | "One failing seed had a dozen faults. Which ones matter?" | `./bin/kuru shrink $FAIL --parallel 2` | `minimal: 0 faults (from 12) — no faults needed — 8 runs (message faults off)` (prepped: `grep minimal: $FAIL/shrink.log`). Say: "None. With this bug a follower answers reads from its own copy, which lags the leader even on a healthy network." |
| 1:20 | "Here is the failing run on the timeline." | browser → run list → `$FAIL` → **▶ play** | partition bands appear, messages across them become red ✕, ops outlined red |
| 1:40 | "Jump straight to the violation." | **⚑ jump to first violation** | playhead at the read outlined red. Click it: "R k…→… on n… Flagged by linearizable-kv: stale read…"; the cluster box shows the leader (★) |
| 1:50 | "And in plain English, why did it fail?" | `./bin/kuru explain --exact $FAIL` (prepped: `cat $FAIL/explain.md`; the minimal run's story is `$FAIL-min/explain.md`) | "**linearizable-kv** flagged this: client … read `k…` from n… and got `…`. But client …'s write of `…` had already been acknowledged at step …", who the leader was, and **Why:** the node answered from its local copy (cut off by a partition, or simply lagging). The same text is in the viewer's **Why it failed** box. |
| 2:00 | "Replay forces the recorded decisions and reproduces the failure." | `./bin/kuru replay $FAIL` (prepped), then **vs original** in the run list | `verdict FAIL (original FAIL): reproduced`, fidelity in the header, both timelines synced. Fidelity on toy KV is low (7–60% measured) because Raft's randomized election timers are not under kuru's control; the fake node replays at 99.8–100% (`scripts/engine-replay-check.sh`). Don't claim "deterministic" for Raft. |
| 2:30 | "Now a bug that needs clock drift." | run list → filter `toykv-lease-clock` → a FAIL run | inside a partition band, `⏱` skew badges on the isolated old leader, a stale read after the new leader (★) is elected; **Why it failed** says "its clock jumped back … ms at step …". Prepped: 15 of 20 seeds FAIL |
| 3:00 | "And a real system: etcd 2.3.8, the stale reads Jepsen found in 2014." | run list → `real-etcd-buggy` FAIL, then `real-etcd-fixed` | buggy: `stale read of k… on n…` (prepped 20 of 20 FAIL; 196 of 200 at scale); fixed (etcd 3.5.17): PASS on the same seeds (20 of 20; 200 of 200 at scale) |
| 3:30 | "The correct build passes 200 seeds." | run list → filter `toykv-correct`, verdict PASS | 200 of 200 PASS |
| 3:50 | "Explore, shrink, explain, replay, see it." | – | – |

No laptop at all? The **Live demo** link in the README has the stale-read, lease-clock, etcd and correct runs bundled as static files.

If the engine's real-etcd runs are not ready, show `ack-before-commit` instead: run list → `toykv-lost-write` FAIL → durability "acknowledged write k… lost".

## Commands with known output (no engine needed)

These run from a clean checkout and are safe to show live.

```sh
make build
cp -R testdata/sample-run /tmp/sample && ./bin/kuru check /tmp/sample --checkers linearizable-kv,durability
```

```
CHECKER          VERDICT  REASON                                           BAD OPS
linearizable-kv  FAIL     stale read of k0 on n1: got c0-1, expected c1-1  4,5
durability       PASS     -                                                -
verdict: FAIL
linearizability: linearizability.html
```

```sh
go run ./scripts/brain-fakerun -scenario examples/fake-faults.yaml -seed 1 -runs-dir /tmp/runs
./bin/kuru shrink /tmp/runs/fake-faults/1
```

```
shrinking /tmp/runs/fake-faults/1 with runner fake-faults (max 60 runs, 1 parallel)
minimal: 2 faults (from 8) — crash n3 at step 195 for 1; partition n1 | n2,n3 at step 420 for 1 — 36 runs (message faults off)
```

## Backup plan (Docker slow or broken on stage)

Everything above is already in `runs/` from the prep script. Do not run anything live:

1. `./bin/kuru serve --open`. The viewer only needs the run folders, not Docker.
2. For each "Do" step, open the prepped run instead of running the command. Show `cat <run>/shrink.log | tail -1` for the shrink result.
3. If `runs/` is lost, `scripts/brain-demo-prep.sh --no-docker` rebuilds the sample and a shrunk fake-faults run in about 30 seconds. `?run=sample` shows the stale read on the timeline with the partition, the n3 crash, drops, a delayed and a duplicated message, and the bad read outlined red.
4. To show real systems without the engine: `go test -tags integration -v -run 'Etcd.*Partition' ./pkg/workload` (about 45 s). It prints `kuru/etcd:buggy … linearizable-kv FAIL "stale read …"` and `kuru/etcd:fixed … PASS`.
