# Kurukshetra node protocol

Every node image used with Kurukshetra (the fake node now, Person B's toy KV later) follows this.

## Environment variables (set by the engine)

- `NODE_ID` — `n1`..`nN`
- `PEER_LISTEN` — address for node-to-node traffic, e.g. `:7000`
- `CLIENT_LISTEN` — address for the client API, e.g. `:8080`
- `PEERS` — comma list `id=host:port` for every OTHER node; the address this node must use to reach that peer. The engine sets these to proxy addresses, so a node must never discover peers any other way.
- `DATA_DIR` — persistent directory (survives crash/restart), default `/data`
- `KURU_CLOCK_OFFSET_MS` — initial clock offset, default `0`
- `BUG_*` — optional bug flags (target-specific)

## Node-to-node traffic (message mode)

Every message is one HTTP request `POST http://<peer addr>/peer/<rpc-name>` with header `X-Kuru-From: <sender id>` and an opaque body. The response body is the RPC reply. One request = one message. The network may delay, drop (the sender sees HTTP 504 or a timeout), or duplicate requests, so nodes must tolerate 5xx errors, timeouts and duplicate requests.

## Client API (on `CLIENT_LISTEN`), all JSON

| Request | Success | Errors |
| --- | --- | --- |
| `GET /kv/{key}` | `200 {"value":"..."}` | `404 {"error":"not_found"}` (means value is missing, counts as a successful read of `""`); `421 {"error":"not_leader","leader":"n2"}`; `503 {"error":"unavailable"}` |
| `PUT /kv/{key}` body `{"value":"v"}` | `200 {"ok":true}` | `421`, `503` as above |
| `POST /kv/{key}/cas` body `{"expect":"a","value":"b"}` | `200 {"ok":true}` | `409 {"error":"cas_mismatch"}`; `421`; `503` |

How a client must record results: `200` → `ok`; `404` on read → `ok` with value `""`; `409`, `421`, `503` → `fail` (definitely not applied); timeout, connection error, any other status → `info`.

## Kuru hooks

- `GET /kuru/health` → `200 {"node":"n1","state":"leader|follower|candidate|single","leader":"n2","term":5}` once the node can serve requests.
- `POST /kuru/clock` body `{"offset_ms":250}` → `200 {"ok":true}`; sets the node's logical clock offset. Nodes that cannot support it return `404`.

## Stream mode (`mode: tcp`)

For black-box targets that speak raw TCP; nodes still read `PEERS`, and peer traffic goes through the proxy as TCP streams.
