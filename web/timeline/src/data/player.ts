// Player state (a playhead over the step axis) and the cluster state at a step.
import type { RunModel } from "./model";

/** Steps per second at 1× speed. */
export const BASE_STEPS_PER_SEC = 100;
export const SPEEDS = [1, 2, 5, 10, 20, 50] as const;

export interface PlayerState {
  step: number;
  playing: boolean;
  speed: number;
  total: number;
}

export type PlayerAction =
  | { type: "play" }
  | { type: "pause" }
  | { type: "toggle" }
  | { type: "step"; delta: number }
  | { type: "seek"; step: number }
  | { type: "speed"; speed: number }
  | { type: "tick"; ms: number }
  | { type: "total"; total: number };

export function initialPlayer(total: number, step = 0): PlayerState {
  return { step: clampStep(step, total), playing: false, speed: 1, total };
}

function clampStep(s: number, total: number): number {
  if (!Number.isFinite(s)) return 0;
  return Math.min(Math.max(s, 0), total);
}

/** playerReducer is the pure state machine behind the player controls. */
export function playerReducer(s: PlayerState, a: PlayerAction): PlayerState {
  switch (a.type) {
    case "play":
      // Playing from the end restarts from 0.
      return { ...s, playing: true, step: s.step >= s.total ? 0 : s.step };
    case "pause":
      return { ...s, playing: false };
    case "toggle":
      return playerReducer(s, { type: s.playing ? "pause" : "play" });
    case "step":
      return { ...s, playing: false, step: clampStep(Math.round(s.step) + a.delta, s.total) };
    case "seek":
      return { ...s, step: clampStep(a.step, s.total) };
    case "speed":
      return { ...s, speed: Math.min(Math.max(a.speed, 1), 50) };
    case "tick": {
      if (!s.playing) return s;
      const step = s.step + (a.ms / 1000) * BASE_STEPS_PER_SEC * s.speed;
      if (step >= s.total) return { ...s, step: s.total, playing: false };
      return { ...s, step };
    }
    case "total":
      return { ...s, total: a.total, step: clampStep(s.step, a.total) };
  }
}

/** keyAction maps a keyboard event to a player action (space, ←/→, shift+←/→). */
export function keyAction(key: string, shift: boolean): PlayerAction | null {
  switch (key) {
    case " ":
    case "Spacebar":
      return { type: "toggle" };
    case "ArrowLeft":
      return { type: "step", delta: shift ? -100 : -1 };
    case "ArrowRight":
      return { type: "step", delta: shift ? 100 : 1 };
  }
  return null;
}

export type NodeStatus = "up" | "down" | "paused";

/** ClusterState is what the cluster looks like at one step. */
export interface ClusterState {
  step: number;
  nodes: { name: string; status: NodeStatus; offsetMs: number }[];
  partition: string[][] | null; // active partition groups
  leader: string; // "" if unknown
}

const inside = (step: number, iv: { start: number; end: number; open: boolean }) => step >= iv.start && (step < iv.end || (iv.open && step <= iv.end));

/** clusterStateAt derives node status, partitions, clock offsets and the leader at step. */
export function clusterStateAt(m: RunModel, step: number): ClusterState {
  const nodes = m.nodes.map((name) => {
    let status: NodeStatus = "up";
    if (m.down.some((iv) => iv.node === name && inside(step, iv))) status = "down";
    else if (m.paused.some((iv) => iv.node === name && inside(step, iv))) status = "paused";
    let offsetMs = 0;
    for (const s of m.skews) if (s.node === name && s.step <= step) offsetMs = s.offsetMs;
    return { name, status, offsetMs };
  });
  const part = m.partitions.filter((iv) => inside(step, iv));
  let leader = "";
  for (const n of m.leaders) if (n.step <= step) leader = n.leader;
  return { step, nodes, partition: part.length ? part[part.length - 1].groups ?? [] : null, leader };
}
