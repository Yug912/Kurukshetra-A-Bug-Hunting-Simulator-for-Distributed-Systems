// A deterministic generator of large synthetic runs, for performance tests and demos.
import type { HistoryOp, RunData, TraceEvent } from "./types";

/** mulberry32 is a tiny seeded PRNG. */
function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** syntheticRun builds a run with about `events` trace events over `nodes` nodes. */
export function syntheticRun(events: number, nodes = 5, seed = 1): RunData {
  const rnd = mulberry32(seed);
  const names = Array.from({ length: nodes }, (_, i) => `n${i + 1}`);
  const trace: TraceEvent[] = [];
  let step = 0;
  let n = 0;
  const push = (e: Omit<TraceEvent, "seq" | "wall_ns">) => trace.push({ ...e, seq: trace.length, wall_ns: 0 });
  push({ step: 0, kind: "run_start", meta: { mode: "http" } });
  while (trace.length < events) {
    step += rnd() < 0.5 ? 0 : 1;
    if (rnd() < 0.002) {
      push({ step, kind: "partition", meta: { groups: `${names[0]}|${names.slice(1).join(",")}` } });
      push({ step: step + 50, kind: "heal" });
      continue;
    }
    const from = names[Math.floor(rnd() * nodes)];
    let to = names[Math.floor(rnd() * nodes)];
    if (to === from) to = names[(names.indexOf(from) + 1) % nodes];
    const id = `${from}-${to}-${++n}`;
    const path = rnd() < 0.7 ? "/peer/append_entries" : "/peer/request_vote";
    const base = { from, to, msg_id: id, size: 64, meta: { path } };
    push({ step, kind: "msg_send", ...base });
    const x = rnd();
    if (x < 0.03) push({ step: step + 1, kind: "msg_drop", ...base, meta: { path, reason: "random" } });
    else if (x < 0.06) {
      push({ step: step + 1, kind: "msg_delay", ...base, meta: { path, steps: "15" } });
      push({ step: step + 16, kind: "msg_deliver", ...base });
    } else if (x < 0.08) {
      push({ step: step + 1, kind: "msg_dup", ...base });
      push({ step: step + 2, kind: "msg_deliver", ...base });
      push({ step: step + 3, kind: "msg_deliver", ...base });
    } else push({ step: step + 2, kind: "msg_deliver", ...base });
  }
  trace.sort((a, b) => a.step - b.step || a.seq - b.seq);
  trace.forEach((e, i) => (e.seq = i));
  const end = trace[trace.length - 1].step + 5;
  push({ step: end, kind: "run_end" });

  const history: HistoryOp[] = [];
  for (let c = 0; c < 4; c++) {
    for (let s = c * 3; s < end - 20; s += 20 + Math.floor(rnd() * 20)) {
      const op = { client: c, f: "write" as const, key: `k${c % 2}`, value: `c${c}-${s}`, node: names[c % nodes], wall_ns: 0 };
      history.push({ ...op, index: 0, type: "invoke", step: s });
      history.push({ ...op, index: 0, type: rnd() < 0.9 ? "ok" : "info", step: s + 5 + Math.floor(rnd() * 10) });
    }
  }
  history.sort((a, b) => a.step - b.step);
  history.forEach((o, i) => (o.index = i));
  return {
    id: "synthetic",
    base: "",
    meta: { scenario: "synthetic", seed, nodes: names, mode: "http", runner: "synthetic", started_at: "", duration_ms: 0, steps: end, version: "synthetic" },
    result: null,
    faults: [],
    trace,
    history,
  };
}
