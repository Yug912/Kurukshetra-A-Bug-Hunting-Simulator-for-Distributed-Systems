// Pure functions that turn raw trace/history records into things the timeline draws.
import type { HistoryOp, RunResult, TraceEvent } from "./types";

/** A message from msg_send to its fate (delivered, dropped, duplicated, delayed). */
export interface Message {
  id: string;
  from: string;
  to: string;
  path: string;
  size: number;
  send: number; // step of msg_send
  deliveries: number[]; // steps of msg_deliver (2+ when duplicated)
  drop: number | null; // step of msg_drop
  dropReason: string;
  delaySteps: number; // from msg_delay meta.steps (0 = not delayed)
  dup: boolean;
  end: number; // last step this message is drawn to
  events: TraceEvent[];
}

/** pairMessages groups msg_* events by msg_id, in order of first appearance. */
export function pairMessages(trace: TraceEvent[]): Message[] {
  const byId = new Map<string, Message>();
  const out: Message[] = [];
  for (const e of trace) {
    if (!e.kind.startsWith("msg_") || !e.msg_id) continue;
    let m = byId.get(e.msg_id);
    if (!m) {
      m = {
        id: e.msg_id,
        from: e.from ?? "",
        to: e.to ?? "",
        path: e.meta?.path ?? "",
        size: e.size ?? 0,
        send: e.step,
        deliveries: [],
        drop: null,
        dropReason: "",
        delaySteps: 0,
        dup: false,
        end: e.step,
        events: [],
      };
      byId.set(e.msg_id, m);
      out.push(m);
    }
    m.events.push(e);
    switch (e.kind) {
      case "msg_send":
        m.send = e.step;
        break;
      case "msg_deliver":
        m.deliveries.push(e.step);
        break;
      case "msg_drop":
        m.drop = e.step;
        m.dropReason = e.meta?.reason ?? "";
        break;
      case "msg_delay":
        m.delaySteps = Number(e.meta?.steps ?? 0) || 1;
        break;
      case "msg_dup":
        m.dup = true;
        break;
    }
    m.end = Math.max(m.end, e.step);
  }
  out.sort((a, b) => a.send - b.send);
  return out;
}

/** An invocation and its completion (null = never completed), like history.Pairs. */
export interface OpPair {
  invoke: HistoryOp;
  complete: HistoryOp | null;
  client: number;
  start: number;
  end: number | null;
  bad: boolean;
}

/** pairOps matches each invoke with the next completion of the same client. */
export function pairOps(ops: HistoryOp[], bad: Set<number> = new Set()): OpPair[] {
  const out: OpPair[] = [];
  const open = new Map<number, number>();
  for (const op of ops) {
    if (op.type === "invoke") {
      open.set(op.client, out.length);
      out.push({ invoke: op, complete: null, client: op.client, start: op.step, end: null, bad: bad.has(op.index) });
      continue;
    }
    const i = open.get(op.client);
    if (i === undefined) continue; // completion without invoke: ignored
    const p = out[i];
    p.complete = op;
    p.end = op.step;
    p.bad = p.bad || bad.has(op.index);
    open.delete(op.client);
  }
  return out;
}

/** Returns every history index listed in any check's bad_ops. */
export function badOpSet(result: RunResult | null): Set<number> {
  const s = new Set<number>();
  for (const c of result?.checks ?? []) for (const i of c.bad_ops ?? []) s.add(i);
  return s;
}

/** A labelled [start, end) step interval. */
export interface Interval {
  start: number;
  end: number;
  node?: string; // for down/paused intervals
  groups?: string[][]; // for partitions
  label: string;
  open: boolean; // true if never closed (runs to the end)
}

/** parseGroups turns "n1|n2,n3" into [["n1"],["n2","n3"]]. */
export function parseGroups(s: string | undefined): string[][] {
  if (!s) return [];
  return s.split("|").map((g) => g.split(",").map((n) => n.trim()).filter(Boolean));
}

/** runEnd is the last step of the run (run_end, else the max step seen, else fallback). */
export function runEnd(trace: TraceEvent[], fallback = 0): number {
  let end = fallback;
  for (const e of trace) end = Math.max(end, e.step);
  return end;
}

/** partitionIntervals pairs each partition with the next heal. */
export function partitionIntervals(trace: TraceEvent[], end = runEnd(trace)): Interval[] {
  const out: Interval[] = [];
  let active: Interval[] = [];
  for (const e of trace) {
    if (e.kind === "partition") {
      const groups = parseGroups(e.meta?.groups);
      const iv: Interval = { start: e.step, end, groups, label: e.meta?.groups ?? "partition", open: true };
      out.push(iv);
      active.push(iv);
    } else if (e.kind === "heal") {
      for (const iv of active) {
        iv.end = e.step;
        iv.open = false;
      }
      active = [];
    }
  }
  return out;
}

/** nodeIntervals pairs a per-node start kind with its end kind (crash→restart, pause→resume). */
export function nodeIntervals(trace: TraceEvent[], startKind: string, endKind: string, end = runEnd(trace)): Interval[] {
  const out: Interval[] = [];
  const active = new Map<string, Interval>();
  for (const e of trace) {
    if (!e.node) continue;
    if (e.kind === startKind && !active.has(e.node)) {
      const iv: Interval = { start: e.step, end, node: e.node, label: `${startKind} ${e.node}`, open: true };
      out.push(iv);
      active.set(e.node, iv);
    } else if (e.kind === endKind) {
      const iv = active.get(e.node);
      if (iv) {
        iv.end = e.step;
        iv.open = false;
        active.delete(e.node);
      }
    }
  }
  return out;
}

export const downIntervals = (t: TraceEvent[], end?: number) => nodeIntervals(t, "crash", "restart", end);
export const pausedIntervals = (t: TraceEvent[], end?: number) => nodeIntervals(t, "pause", "resume", end);

/** A clock skew applied to a node at a step. */
export interface SkewMarker {
  step: number;
  node: string;
  offsetMs: number;
  event: TraceEvent;
}

export function skewMarkers(trace: TraceEvent[]): SkewMarker[] {
  return trace
    .filter((e) => e.kind === "clock_skew")
    .map((e) => ({ step: e.step, node: e.node ?? "", offsetMs: Number(e.meta?.offset_ms ?? 0), event: e }));
}

/** Notes that report a node's view of the leader (meta.leader), used by the player. */
export interface LeaderNote {
  step: number;
  node: string;
  leader: string;
}

export function leaderNotes(trace: TraceEvent[]): LeaderNote[] {
  return trace
    .filter((e) => e.kind === "note" && e.meta?.leader !== undefined)
    .map((e) => ({ step: e.step, node: e.node ?? "", leader: e.meta!.leader }));
}

/** Distinct message paths, sorted (for the "hide by path" filter). */
export function messagePaths(msgs: Message[]): string[] {
  return [...new Set(msgs.map((m) => m.path))].sort();
}

/** Short label for an op: "W k0=c1-1", "R k0→c0-1", "CAS k0 a→b". */
export function opLabel(p: OpPair): string {
  const inv = p.invoke;
  switch (inv.f) {
    case "write":
      return `W ${inv.key}=${inv.value ?? ""}`;
    case "read": {
      if (p.complete?.type === "ok") return `R ${inv.key}→${p.complete.value || "∅"}`;
      return `R ${inv.key}?`;
    }
    default:
      return `CAS ${inv.key} ${inv.expect || "∅"}→${inv.value ?? ""}`;
  }
}

/** Outcome of an op pair: ok | fail | info (info also when never completed). */
export function opOutcome(p: OpPair): "ok" | "fail" | "info" {
  const t = p.complete?.type;
  return t === "ok" || t === "fail" ? t : "info";
}

/** First step of a bad op's invoke, or null. */
export function firstViolationStep(pairs: OpPair[]): number | null {
  let s: number | null = null;
  for (const p of pairs) if (p.bad && (s === null || p.start < s)) s = p.start;
  return s;
}

/** Sorted distinct client ids. */
export function clientIds(pairs: OpPair[]): number[] {
  return [...new Set(pairs.map((p) => p.client))].sort((a, b) => a - b);
}
