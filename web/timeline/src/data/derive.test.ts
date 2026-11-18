import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  badOpSet,
  downIntervals,
  firstViolationStep,
  opLabel,
  opOutcome,
  pairMessages,
  pairOps,
  parseGroups,
  partitionIntervals,
  pausedIntervals,
  skewMarkers,
} from "./derive";
import { parseJSONL } from "./jsonl";
import { buildModel } from "./model";
import { syntheticRun } from "./synthetic";
import type { HistoryOp, RunData, RunResult, TraceEvent } from "./types";
import { centerOn, clamp, decimate, pan, scaleX, visibleRange, zoomAt } from "./viewport";

const sampleDir = join(__dirname, "..", "..", "..", "..", "testdata", "sample-run");
const read = (f: string) => readFileSync(join(sampleDir, f), "utf8");

function sample(): RunData {
  return {
    id: "sample",
    base: "",
    meta: JSON.parse(read("meta.json")),
    result: JSON.parse(read("result.json")),
    faults: JSON.parse(read("faults.json")),
    trace: parseJSONL<TraceEvent>(read("trace.jsonl")),
    history: parseJSONL<HistoryOp>(read("history.jsonl")),
  };
}

const ev = (step: number, kind: TraceEvent["kind"], extra: Partial<TraceEvent> = {}): TraceEvent => ({ seq: 0, step, wall_ns: 0, kind, ...extra });

describe("parseJSONL", () => {
  it("skips blank lines and reports bad lines", () => {
    expect(parseJSONL<{ a: number }>('{"a":1}\n\n {"a":2} \n')).toEqual([{ a: 1 }, { a: 2 }]);
    expect(() => parseJSONL('{"a":1}\n{bad')).toThrow(/line 2/);
  });
});

describe("pairMessages", () => {
  it("pairs the sample's sends with deliveries, drops, delays and dups", () => {
    const msgs = pairMessages(sample().trace);
    const byId = new Map(msgs.map((m) => [m.id, m]));
    expect(byId.get("n1-n2-1")).toMatchObject({ from: "n1", to: "n2", send: 10, deliveries: [12], drop: null, path: "/peer/append_entries" });
    expect(byId.get("n1-n2-4")).toMatchObject({ drop: 131, dropReason: "partition", deliveries: [] });
    expect(byId.get("n2-n3-2")).toMatchObject({ delaySteps: 15, deliveries: [261] });
    expect(byId.get("n3-n2-2")).toMatchObject({ dup: true, deliveries: [253, 255] });
    expect(byId.get("n2-n3-7")).toMatchObject({ drop: 721, dropReason: "crash" });
    for (let i = 1; i < msgs.length; i++) expect(msgs[i].send).toBeGreaterThanOrEqual(msgs[i - 1].send);
  });

  it("handles a delivery without a send", () => {
    const [m] = pairMessages([ev(5, "msg_deliver", { msg_id: "x", from: "a", to: "b" })]);
    expect(m).toMatchObject({ send: 5, deliveries: [5] });
  });
});

describe("pairOps", () => {
  it("pairs per client and marks bad ops", () => {
    const s = sample();
    const pairs = pairOps(s.history, badOpSet(s.result));
    expect(pairs).toHaveLength(7);
    const bad = pairs.filter((p) => p.bad);
    expect(bad.map((p) => p.invoke.index)).toEqual([4]);
    expect(opLabel(bad[0])).toBe("R k0→c0-1");
    expect(opLabel(pairs[0])).toBe("W k0=c0-1");
    expect(firstViolationStep(pairs)).toBe(400);
    expect(pairs.filter((p) => p.invoke.final)).toHaveLength(3);
  });

  it("leaves a never-completed op open and classifies outcomes", () => {
    const ops: HistoryOp[] = [
      { index: 0, client: 0, type: "invoke", f: "write", key: "k", value: "v", node: "n1", step: 1, wall_ns: 0 },
      { index: 1, client: 1, type: "invoke", f: "cas", key: "k", expect: "a", value: "b", node: "n1", step: 2, wall_ns: 0 },
      { index: 2, client: 1, type: "fail", f: "cas", key: "k", node: "n1", step: 3, wall_ns: 0 },
      { index: 3, client: 2, type: "ok", f: "read", key: "k", node: "n1", step: 3, wall_ns: 0 },
    ];
    const pairs = pairOps(ops);
    expect(pairs).toHaveLength(2);
    expect(pairs[0].complete).toBeNull();
    expect(opOutcome(pairs[0])).toBe("info");
    expect(opOutcome(pairs[1])).toBe("fail");
    expect(opLabel(pairs[1])).toBe("CAS k a→b");
  });
});

describe("intervals", () => {
  it("computes the sample's partition and crash", () => {
    const t = sample().trace;
    expect(partitionIntervals(t)).toEqual([{ start: 120, end: 600, groups: [["n1"], ["n2", "n3"]], label: "n1|n2,n3", open: false }]);
    expect(downIntervals(t)).toEqual([{ start: 700, end: 800, node: "n3", label: "crash n3", open: false }]);
    expect(pausedIntervals(t)).toEqual([]);
  });

  it("runs unclosed intervals to the end and handles pauses and skew", () => {
    const t = [
      ev(10, "pause", { node: "n2" }),
      ev(20, "resume", { node: "n2" }),
      ev(30, "crash", { node: "n1" }),
      ev(40, "partition", { meta: { groups: "n1,n2|n3" } }),
      ev(50, "clock_skew", { node: "n3", meta: { offset_ms: "-250" } }),
      ev(90, "run_end"),
    ];
    expect(pausedIntervals(t)).toMatchObject([{ start: 10, end: 20, node: "n2", open: false }]);
    expect(downIntervals(t)).toMatchObject([{ start: 30, end: 90, open: true }]);
    expect(partitionIntervals(t)).toMatchObject([{ start: 40, end: 90, open: true, groups: [["n1", "n2"], ["n3"]] }]);
    expect(skewMarkers(t)).toMatchObject([{ step: 50, node: "n3", offsetMs: -250 }]);
    expect(parseGroups(undefined)).toEqual([]);
  });
});

describe("badOpSet", () => {
  it("unions bad ops across checks", () => {
    const r: RunResult = { seed: 1, scenario: "s", verdict: "FAIL", checks: [{ checker: "a", verdict: "FAIL", bad_ops: [1, 2] }, { checker: "b", verdict: "FAIL", bad_ops: [2, 7] }] };
    expect([...badOpSet(r)].sort()).toEqual([1, 2, 7]);
    expect(badOpSet(null).size).toBe(0);
  });
});

describe("viewport", () => {
  it("zooms around an anchor and clamps", () => {
    const vp = zoomAt({ start: 0, end: 1000 }, 0.5, 500, 1000);
    expect(vp).toEqual({ start: 250, end: 750 });
    expect(zoomAt(vp, 10, 500, 1000)).toEqual({ start: 0, end: 1000 });
    expect(pan({ start: 0, end: 100 }, -50, 1000)).toEqual({ start: 0, end: 100 });
    expect(pan({ start: 0, end: 100 }, 5000, 1000)).toEqual({ start: 900, end: 1000 });
    expect(clamp({ start: 5, end: 6 }, 1000).end - 5).toBe(10);
    expect(centerOn({ start: 0, end: 100 }, 500, 1000)).toEqual({ start: 450, end: 550 });
    expect(scaleX({ start: 100, end: 200 }, 1000)(150)).toBe(500);
  });

  it("finds visible items with a binary search", () => {
    const items = Array.from({ length: 100 }, (_, i) => ({ s: i * 10, e: i * 10 + 15 }));
    const v = visibleRange(items, 100, 120, (x) => x.s, (x) => x.e, 15);
    expect(v.map((x) => x.s)).toEqual([90, 100, 110, 120]);
    expect(decimate(items, 10).items).toHaveLength(10);
    expect(decimate(items, 1000).stride).toBe(1);
  });
});

describe("large runs", () => {
  it("builds a 50,000-event model and queries a window quickly", () => {
    const run = syntheticRun(50_000);
    expect(run.trace.length).toBeGreaterThanOrEqual(50_000);
    const t0 = performance.now();
    const m = buildModel(run);
    const built = performance.now() - t0;
    const t1 = performance.now();
    let n = 0;
    for (let s = 0; s < m.totalSteps; s += m.totalSteps / 50) {
      n += visibleRange(m.messages, s, s + 500, (x) => x.send, (x) => x.end, m.maxMsgSpan).length;
    }
    const queried = (performance.now() - t1) / 50;
    expect(m.messages.length).toBeGreaterThan(15_000);
    expect(n).toBeGreaterThan(0);
    expect(built).toBeLessThan(1000);
    expect(queried).toBeLessThan(5);
  });

  it("builds the sample model", () => {
    const m = buildModel(sample());
    expect(m.nodes).toEqual(["n1", "n2", "n3"]);
    expect(m.clients).toEqual([0, 1, 2]);
    expect(m.totalSteps).toBe(1000);
    expect(m.firstViolation).toBe(400);
    expect(m.paths).toEqual(["/peer/append_entries", "/peer/request_vote"]);
  });
});
