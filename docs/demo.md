# Demo: 4 minutes on stage

**Story:** Kurukshetra finds a stale read in a Raft KV store, shrinks the failure to two faults, shows it on a timeline, replays it, then catches a clock-skew bug and a real etcd bug, while the correct build passes 200 seeds.

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
| 0:50 | "One failing seed had nine faults. Which ones matter?" | `./bin/kuru shrink $FAIL --parallel 2` | `minimal: 2 faults (from N) — partition n1 \| n2,n3 at step … for …; crash … — K runs` (takes a while live: use the prepped result, `cat $FAIL/shrink.log \| tail -1`) |
| 1:20 | "Here is the minimal run on the timeline." | browser → run list → the `…-min` run → **▶ play** | partition band appears, messages to n1 become red ✕ |
| 1:40 | "Jump straight to the violation." | **⚑ jump to first violation** | playhead at the read outlined red. Click it: "R k0→c0-1 on n1 … Flagged by linearizable-kv: stale read…" |
| 1:50 | "And in plain English, why did it fail?" | `./bin/kuru explain $FAIL` (prepped: `cat $FAIL-min/explain.md`) | "**linearizable-kv** flagged this: client … read `k…` from n… and got …", the partition that cut that node off, who the leader was, and **Why:** answered from its local copy without a majority. The same text is in the viewer's **Why it failed** box. |
| 2:00 | "Replays are deterministic: same decisions, same failure." | `./bin/kuru replay $FAIL` (prepped), then **vs original** in the run list | the same FAIL, fidelity shown in the header, both timelines synced |
| 2:30 | "Now a bug that needs clock drift." | run list → filter `toykv-lease-clock` → a FAIL run | a negative `⏱` skew badge (backward jump) on the isolated old leader, stale read after the new leader is elected |
| 3:00 | "And a real system: etcd 2.3.8, the stale reads Jepsen found in 2014." | run list → `real-etcd-buggy` FAIL, then `real-etcd-fixed` | buggy: `stale read of k0 on n3`; fixed (etcd 3.5.17): PASS on the same seeds |
| 3:30 | "The correct build passes 200 seeds." | run list → filter `toykv-correct`, verdict PASS | 200 of 200 PASS |
| 3:50 | "Explore, shrink, replay, see it." | – | – |

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
