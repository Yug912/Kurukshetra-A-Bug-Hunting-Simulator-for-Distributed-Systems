# etcd: real-world target

Kurukshetra's real-world target is **etcd**. The old **v2.3.8** (`kuru/etcd:buggy`) serves **stale reads** under a partition. The newer **v3.5.17** (`kuru/etcd:fixed`) stays linearizable under the same faults.

## Why etcd

Requirements from the brief, and how etcd meets them:

| Requirement | etcd |
| --- | --- |
| Runs as 3 nodes in Docker easily | A single static binary. Members are configured with flags, and the peer URLs are set explicitly, so a proxy can sit between them |
| Documented consistency bug that a key-value register workload can hit | [Jepsen: etcd and Consul (Kyle Kingsbury, 2014)](https://aphyr.com/posts/316-call-me-maybe-etcd-and-consul) ran a single-register read/write/CAS workload against etcd 0.4.1 under partitions. It found **stale reads**: even with `consistent=true`, a member could answer a read from its local state after a newer value had been written, violating linearizability |
| Fixed newer version | The etcd team added quorum reads ([coreos/etcd#866](https://github.com/coreos/etcd/pull/866), `?quorum=true`), but in the v2 API they stayed **opt-in**: a default v2 `GET` in etcd 2.x is still served from the asked member's local store. etcd 3's v3 API made **linearizable reads the default** (each read goes through ReadIndex/quorum unless `serializable` is requested) |

I picked etcd over the other Jepsen-analysed systems for practical reasons. It has an HTTP/JSON API, so no new Go client library is needed and `go.mod` stays unchanged. It also has static release binaries for both versions, and the Jepsen bug shows up with exactly the register workload Kurukshetra already has. Consul's 2014 stale-read finding is similar, but its fix is also an opt-in read mode. The [etcd 3.4.3 analysis (2020)](https://jepsen.io/analyses/etcd-3.4.3) found problems in watches and locks, not in KV reads.

**The bug we reproduce.** The workload sends a plain `GET /v2/keys/<k>` to etcd 2.3.8, which is the default the Jepsen post warns about. The `etcd-kv` workload sends reads, writes and CAS ops to random members. When one member is cut off from its peers but clients can still reach it, it keeps answering reads from its stale local store while the majority accepts new writes. `linearizable-kv` FAILs with "stale read of k0 on n3: got c1-29, expected c3-27". Against etcd 3.5.17 the same workload uses the v3 JSON gateway (`/v3/kv/range|put|txn`). There, reads on the cut-off member time out (recorded as `info`) instead of returning old data, and the run PASSes.

Honest caveats:

- The "buggy" version is v2.3.8, not 0.4.1. It is the last 2.x release, it builds from source today, and its default reads have the same Jepsen-documented behaviour.
- The comparison is "v2 default reads" against "v3 default reads". Strictly, etcd's fix is a semantics change in the newer API, not a patch to the old read path.

## Images

```sh
docker build -t kuru/etcd:buggy -f targets/etcd/Dockerfile.buggy .   # etcd v2.3.8, built from pinned source
docker build -t kuru/etcd:fixed -f targets/etcd/Dockerfile.fixed .   # etcd v3.5.17 release binary (amd64/arm64)
# or: scripts/brain-build-images.sh
```

- `Dockerfile.buggy` compiles the v2.3.8 source tarball (sha256 pinned) with Go 1.10 for the host architecture. The official v2.3.8 release is a Go 1.7 amd64 binary that crashes under qemu emulation on arm64 hosts (`runtime: newosproc errno=22`), and the old quay.io image uses a manifest format that containerd 2.1+ rejects. etcd 2.x also needs `ETCD_UNSUPPORTED_ARCH=arm64` to start on arm64; the image sets it.
- `Dockerfile.fixed` downloads the v3.5.17 release binary and verifies its sha256 for amd64 or arm64.

## kuru-etcd-shim

etcd derives member IDs from the peer URLs in `--initial-cluster`, so every member must name each peer by the same URL. Kurukshetra instead hands each node its own address for each peer (its proxy). The shim (`targets/etcd/shim`, the container entrypoint) bridges the two:

1. Every member advertises `http://<id>:<7100+i>` (`i` = index in the sorted ID list). All IDs resolve to `127.0.0.1` through `/etc/hosts`.
2. The shim forwards `127.0.0.1:<7100+i>` to `PEERS[<id>]` (TCP), or to its own `PEER_LISTEN` port for itself. etcd listens for peers on `PEER_LISTEN`.
3. On `CLIENT_LISTEN` it serves `GET /kuru/health` (role, leader name and term from `/v3/maintenance/status` or `/v2/stats/self`; `"starting"` until etcd answers). It answers `POST /kuru/clock` with **404** because etcd has no logical clock to skew, and reverse-proxies everything else to etcd on `127.0.0.1:2379`.

etcd's peer traffic is long-lived HTTP streams (rafthttp), not one request per message, so the scenarios use **`mode: tcp`** (Person A's stream mode).

## Workload `etcd-kv`

`pkg/workload/etcd.go` is the same register workload as `kv` (same op mix, unique values, rate limit, final reads), speaking etcd's API. The API is chosen per node from `GET /version`: 2.x → v2 keys API, 3.x → v3 gateway (base64 keys/values). Results:

| | ok | fail (definitely not applied) | info (unknown) |
| --- | --- | --- | --- |
| v2 read | 200; 404 + errorCode 100 → `""` | – | timeout, 5xx, others |
| v2 write / CAS | 200/201 | CAS 412 (101 compare failed, 105 exists) or 404 (100); other 4xx | timeout, 5xx |
| v3 read | 200 (no kvs → `""`) | 4xx | timeout, 5xx |
| v3 write / CAS | 200 (`succeeded:true` for txn) | txn `succeeded:false`; 4xx | timeout, 5xx |

A CAS with expect `""` means "key missing": `prevExist=false` in v2, `create_revision == 0` in v3.

## Evidence without Person A's engine

`targets/etcd/docker-compose.yml` runs 3 members wired directly. Peer traffic goes over a separate `peers` network, so `docker network disconnect` isolates a member from its peers but not from clients. `go test -tags integration -run 'Etcd.*Partition' ./pkg/workload` brings up each version and runs `etcd-kv` (4 clients, 2 keys, 60 ops/s) for 14 s, cutting one follower off from 3 s to 9 s, then runs final reads and both checkers. Over 4 runs of each version:

| Image | linearizable-kv | durability |
| --- | --- | --- |
| `kuru/etcd:buggy` (2.3.8) | **FAIL** 4/4, e.g. "stale read of k0 on n1: got c1-29, expected c1-30" | PASS |
| `kuru/etcd:fixed` (3.5.17) | **PASS** 4/4 | PASS |

With the engine: `kuru explore examples/real-etcd-buggy.yaml --runs 200` should FAIL on some seeds, and `examples/real-etcd-fixed.yaml` should pass those same seeds (pending Person A's engine on `main`).

## Results at scale (2026-11-20)

`kuru explore examples/real-etcd-buggy.yaml --runs 200 --parallel 4`, then the same 200 seeds (same fault plans: partitions every 300–600 steps for 300–600, crashes every 800–1200 for 200–400) on `examples/real-etcd-fixed.yaml`. MacBook Air (Apple silicon), Colima 4 CPUs, both images built locally with `scripts/brain-build-images.sh` (the v3.5.17 release download worked; no Dockerfile change needed).

| Image | Seeds | FAIL | PASS | UNKNOWN | Time |
| --- | --- | --- | --- | --- | --- |
| `kuru/etcd:buggy` (v2.3.8, v2 API default reads) | 200 | **196** | 1 | 3 | 11 m 38 s |
| `kuru/etcd:fixed` (v3.5.17, v3 API linearizable reads) | 200 | 0 | **200** | 0 | 11 m 16 s |

- Every buggy FAIL is `linearizable-kv` ("stale read of k0 on n2: got c1-18, expected c0-19"); `durability` PASSed in all 400 runs.
- The 3 UNKNOWN are the linearizability checker's 10 s budget (`reason: timeout`), not errors. The one PASS (seed 197) had no read land on a cut-off member while it was stale.
- `kuru explain` on a FAIL tells the Jepsen story in the run's own terms: "n2 was on the minority side of the partition that started at step 1272, so it could not hear about newer writes, yet it still answered the read from its local copy."
