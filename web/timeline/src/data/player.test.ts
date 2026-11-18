import { describe, expect, it } from "vitest";
import { EMPTY_FILTERS } from "../view/filters";
import { buildModel } from "./model";
import { BASE_STEPS_PER_SEC, clusterStateAt, initialPlayer, keyAction, playerReducer } from "./player";
import type { RunData, TraceEvent } from "./types";
import { formatUrl, minOriginal, parseUrl, runIdFromPath } from "./url";

describe("playerReducer", () => {
  const s0 = initialPlayer(1000);

  it("steps, seeks and clamps", () => {
    expect(playerReducer(s0, { type: "step", delta: 1 }).step).toBe(1);
    expect(playerReducer(s0, { type: "step", delta: -1 }).step).toBe(0);
    expect(playerReducer({ ...s0, step: 950 }, { type: "step", delta: 100 }).step).toBe(1000);
    expect(playerReducer(s0, { type: "seek", step: 400 }).step).toBe(400);
    expect(playerReducer(s0, { type: "seek", step: -5 }).step).toBe(0);
    expect(playerReducer(s0, { type: "seek", step: NaN }).step).toBe(0);
    expect(initialPlayer(100, 500).step).toBe(100);
  });

  it("plays, ticks at speed and stops at the end", () => {
    let s = playerReducer(s0, { type: "play" });
    expect(s.playing).toBe(true);
    s = playerReducer(s, { type: "tick", ms: 1000 });
    expect(s.step).toBe(BASE_STEPS_PER_SEC);
    s = playerReducer(s, { type: "speed", speed: 10 });
    s = playerReducer(s, { type: "tick", ms: 500 });
    expect(s.step).toBe(BASE_STEPS_PER_SEC + 5 * BASE_STEPS_PER_SEC);
    s = playerReducer(s, { type: "tick", ms: 60_000 });
    expect(s).toMatchObject({ step: 1000, playing: false });
    // play at the end restarts
    expect(playerReducer(s, { type: "play" }).step).toBe(0);
    // ticks while paused do nothing; stepping pauses
    const p = playerReducer({ ...s0, playing: true, step: 10 }, { type: "step", delta: 1 });
    expect(p).toMatchObject({ playing: false, step: 11 });
    expect(playerReducer(p, { type: "tick", ms: 1000 }).step).toBe(11);
    expect(playerReducer(s0, { type: "speed", speed: 500 }).speed).toBe(50);
    expect(playerReducer(s0, { type: "toggle" }).playing).toBe(true);
    expect(playerReducer({ ...s0, step: 900 }, { type: "total", total: 500 }).step).toBe(500);
  });

  it("maps keys", () => {
    expect(keyAction(" ", false)).toEqual({ type: "toggle" });
    expect(keyAction("ArrowLeft", false)).toEqual({ type: "step", delta: -1 });
    expect(keyAction("ArrowRight", true)).toEqual({ type: "step", delta: 100 });
    expect(keyAction("x", false)).toBeNull();
  });
});

describe("clusterStateAt", () => {
  const ev = (step: number, kind: TraceEvent["kind"], extra: Partial<TraceEvent> = {}): TraceEvent => ({ seq: 0, step, wall_ns: 0, kind, ...extra });
  const run: RunData = {
    id: "t",
    base: "",
    meta: { scenario: "t", seed: 1, nodes: ["n1", "n2", "n3"], mode: "http", runner: "x", started_at: "", duration_ms: 0, steps: 100, version: "" },
    result: null,
    faults: [],
    history: [],
    trace: [
      ev(5, "note", { node: "n1", meta: { leader: "n1" } }),
      ev(10, "partition", { meta: { groups: "n1|n2,n3" } }),
      ev(20, "crash", { node: "n3" }),
      ev(25, "note", { node: "n2", meta: { leader: "n2" } }),
      ev(30, "restart", { node: "n3" }),
      ev(40, "heal"),
      ev(50, "pause", { node: "n2" }),
      ev(55, "clock_skew", { node: "n1", meta: { offset_ms: "-300" } }),
      ev(60, "resume", { node: "n2" }),
      ev(70, "clock_skew", { node: "n1", meta: { offset_ms: "0" } }),
      ev(100, "run_end"),
    ],
  };
  const m = buildModel(run);
  const status = (step: number) => clusterStateAt(m, step).nodes.map((n) => n.status);

  it("tracks node status, partitions, offsets and leader", () => {
    expect(clusterStateAt(m, 0)).toMatchObject({ partition: null, leader: "" });
    expect(clusterStateAt(m, 15)).toMatchObject({ partition: [["n1"], ["n2", "n3"]], leader: "n1" });
    expect(status(20)).toEqual(["up", "up", "down"]);
    expect(status(30)).toEqual(["up", "up", "up"]);
    expect(clusterStateAt(m, 26).leader).toBe("n2");
    expect(clusterStateAt(m, 40).partition).toBeNull();
    expect(status(55)).toEqual(["up", "paused", "up"]);
    expect(clusterStateAt(m, 56).nodes[0].offsetMs).toBe(-300);
    expect(clusterStateAt(m, 80).nodes[0].offsetMs).toBe(0);
  });
});

describe("url state", () => {
  it("round-trips", () => {
    const s = {
      run: "toykv-stale-read/7",
      compare: "toykv-stale-read/7-min",
      step: 412.6,
      viewport: { start: 100, end: 900 },
      filters: { ...EMPTY_FILTERS, hiddenNodes: ["n2"], hiddenKinds: ["deliver" as const], hiddenClients: [3], hiddenPaths: ["/peer/append_entries"] },
    };
    const q = formatUrl(s);
    expect(q).toContain("step=413");
    const back = parseUrl("?" + q);
    expect(back).toEqual({ ...s, step: 413 });
  });

  it("ignores junk and keeps empty state small", () => {
    expect(formatUrl(parseUrl("?run=sample"))).toBe("run=sample");
    const s = parseUrl("?step=abc&from=9&to=3&filters=k:bogus,drop;zz");
    expect(s.step).toBeNull();
    expect(s.viewport).toBeNull();
    expect(s.filters.hiddenKinds).toEqual(["drop"]);
  });

  it("derives related run ids", () => {
    expect(runIdFromPath("runs/toykv/6")).toBe("toykv/6");
    expect(runIdFromPath("/abs/runs/toykv/6/")).toBe("toykv/6");
    expect(runIdFromPath("x")).toBeNull();
    expect(runIdFromPath(undefined)).toBeNull();
    expect(minOriginal("toykv/7-min")).toBe("toykv/7");
    expect(minOriginal("toykv/7")).toBeNull();
  });
});
