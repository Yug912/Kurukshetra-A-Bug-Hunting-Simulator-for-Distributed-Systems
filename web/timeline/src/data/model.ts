// RunModel: everything derived from a RunData once, so rendering only filters and draws.
import {
  badOpSet,
  clientIds,
  downIntervals,
  firstViolationStep,
  leaderNotes,
  messagePaths,
  pairMessages,
  pairOps,
  partitionIntervals,
  pausedIntervals,
  runEnd,
  skewMarkers,
  type Interval,
  type LeaderNote,
  type Message,
  type OpPair,
  type SkewMarker,
} from "./derive";
import type { RunData } from "./types";

export interface RunModel {
  run: RunData;
  nodes: string[];
  clients: number[];
  totalSteps: number;
  messages: Message[]; // sorted by send step
  maxMsgSpan: number;
  ops: OpPair[]; // sorted by start step
  maxOpSpan: number;
  partitions: Interval[];
  down: Interval[];
  paused: Interval[];
  skews: SkewMarker[];
  leaders: LeaderNote[];
  paths: string[];
  bad: Set<number>;
  firstViolation: number | null;
}

/** buildModel derives the drawable model of a run. */
export function buildModel(run: RunData): RunModel {
  const total = Math.max(run.history.reduce((m, o) => Math.max(m, o.step), runEnd(run.trace, run.meta.steps ?? 0)), 1);
  const messages = pairMessages(run.trace);
  const bad = badOpSet(run.result);
  const ops = pairOps(run.history, bad).sort((a, b) => a.start - b.start);
  const nodes = run.meta.nodes?.length ? [...run.meta.nodes] : inferNodes(run);
  return {
    run,
    nodes,
    clients: clientIds(ops),
    totalSteps: total,
    messages,
    maxMsgSpan: messages.reduce((m, x) => Math.max(m, x.end - x.send), 0),
    ops,
    maxOpSpan: ops.reduce((m, p) => Math.max(m, (p.end ?? total) - p.start), 0),
    partitions: partitionIntervals(run.trace, total),
    down: downIntervals(run.trace, total),
    paused: pausedIntervals(run.trace, total),
    skews: skewMarkers(run.trace),
    leaders: leaderNotes(run.trace),
    paths: messagePaths(messages),
    bad,
    firstViolation: firstViolationStep(ops),
  };
}

function inferNodes(run: RunData): string[] {
  const s = new Set<string>();
  for (const e of run.trace) for (const n of [e.node, e.from, e.to]) if (n) s.add(n);
  for (const o of run.history) if (o.node) s.add(o.node);
  return [...s].sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
}
