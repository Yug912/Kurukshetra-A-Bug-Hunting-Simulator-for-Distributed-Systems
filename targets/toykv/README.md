# toykv

A small Raft key-value store built on `hashicorp/raft`. It is the default target Kurukshetra hunts bugs in. It follows [docs/PROTOCOL.md](../../docs/PROTOCOL.md) exactly.

## Run it

```sh
docker build -t kuru/toykv -f targets/toykv/Dockerfile .            # from the repo root
docker compose -f targets/toykv/docker-compose.yml up -d            # 3 nodes, no proxy
curl -s localhost:18081/kuru/health
curl -s -XPUT localhost:18081/kv/k0 -d '{"value":"v1"}'             # 421 if n1 is not the leader
```

Locally without Docker: `NODE_ID=n1 PEER_LISTEN=:7001 CLIENT_LISTEN=:8081 PEERS=n2=127.0.0.1:7002,n3=127.0.0.1:7003 DATA_DIR=/tmp/n1 go run ./targets/toykv` (and the same for n2 and n3).

## Environment

| Variable | Meaning |
| --- | --- |
| `NODE_ID` | `n1`..`nN` (required) |
| `PEER_LISTEN` / `CLIENT_LISTEN` | default `:7000` / `:8080` |
| `PEERS` | `id=host:port,...` for every other node. The cluster is bootstrapped from `NODE_ID` + `PEERS` |
| `DATA_DIR` | default `/data`; holds `raft.db` (bolt log + stable store) and snapshots |
| `KURU_CLOCK_OFFSET_MS` | initial logical clock offset |
| `BUG_STALE_READ`, `BUG_ACK_BEFORE_COMMIT`, `BUG_LEASE_READ` | `1` turns a bug on (see below) |
| `LOG_LEVEL` | hashicorp/raft log level, default `INFO` |

## Design

- **Transport.** `HTTPTransport` implements `raft.Transport` + `raft.WithClose`. Each RPC is exactly one HTTP request: `POST http://<peer>/peer/{append_entries,request_vote,install_snapshot,timeout_now}` with `X-Kuru-From: <id>`. The body is the JSON request struct and the response is the JSON response struct. `install_snapshot` carries `{"args":…, "data":<base64 snapshot>}`. Server addresses are node IDs (`"n2"`), mapped to `host:port` through `PEERS`, so every message goes wherever the engine pointed it (the proxy). Pipelining is not supported. Client timeouts are 500 ms for RPCs and 5 s for snapshots. A non-200 status or a timeout is returned as an error, and Raft retries. Duplicate requests are harmless because Raft is idempotent per term and index.
- **Raft config.** Heartbeat and election timeouts are 400 ms, the leader lease is 200 ms, the commit timeout is 20 ms and the snapshot threshold is 1024. Every node bootstraps the same static configuration and ignores `ErrCantBootstrap`, so a restarted node keeps its data.
- **Reads (correct mode).** Only the leader serves `GET`. It puts a `get` command through the Raft log and returns the FSM's answer. A follower answers `421 {"leader":…}`, or `503` if no leader is known.
- **Writes.** The leader calls `Apply` and waits up to 2 s for the commit. A `409` means a CAS mismatch. Status codes map to what the client can know about the outcome:
  - `421` / `503`: rejected before entering the log, so definitely not applied.
  - `500` / `504`: the entry may still commit after leadership was lost or the wait timed out. The workload records these as `info`.

  This differs on purpose from "timeout → 503": a 503 there would tell the checker an op failed when it may have committed, and the correct build would then show false violations.

## Bug flags

| Flag | Behaviour | Violation |
| --- | --- | --- |
| `BUG_STALE_READ=1` | Any node answers `GET` from its local FSM | A partitioned old leader or a lagging follower returns old values → **linearizability** |
| `BUG_ACK_BEFORE_COMMIT=1` | The leader answers `200` to `PUT`/`CAS` right after `Apply`, before the commit | The leader crashes or is cut off before replicating → acknowledged write lost → **durability** (and linearizability) |
| `BUG_LEASE_READ=1` | The leader serves reads locally while `now() < lastAppendEntriesAck + 300ms` | The lease is measured on the skewable clock, so a backwards clock skew plus a partition makes the old leader serve reads after a new leader exists → stale read |

## Clock

`POST /kuru/clock {"offset_ms":N}` sets the node's **logical** clock: `now() = time.Now() + offset`. hashicorp/raft runs its own wall-clock timers (heartbeats, elections, its own leader lease), and toy KV cannot change them. **The offset therefore only affects toy KV's own time logic, namely the `BUG_LEASE_READ` lease.** With the bug flags off, clock skew has no effect on correctness, which is the intended result.

## Tests

- `go test ./targets/toykv` covers the FSM, the transport round trip (all four RPCs, a duplicated request, the heartbeat fast path, errors) and an in-process 3-node cluster: elect, write, read, kill the leader, elect again, read. None of it needs Docker.
- `go test -tags integration ./targets/toykv` runs `docker compose up`, writes and reads, `docker kill`s the leader, waits up to 5 s for a new leader, then reads the data back.
