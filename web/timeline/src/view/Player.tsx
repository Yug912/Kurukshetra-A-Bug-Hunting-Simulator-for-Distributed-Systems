import { useEffect, useReducer } from "react";
import type { RunModel } from "../data/model";
import { clusterStateAt, initialPlayer, keyAction, playerReducer, SPEEDS, type PlayerAction, type PlayerState } from "../data/player";

/** usePlayer runs the player state machine with an animation-frame clock and keyboard shortcuts. */
export function usePlayer(total: number, start: number): [PlayerState, (a: PlayerAction) => void] {
  const [state, dispatch] = useReducer(playerReducer, undefined, () => initialPlayer(total, start));
  useEffect(() => dispatch({ type: "total", total }), [total]);
  useEffect(() => {
    if (!state.playing) return;
    let last = performance.now();
    let raf = requestAnimationFrame(function frame(now) {
      dispatch({ type: "tick", ms: now - last });
      last = now;
      raf = requestAnimationFrame(frame);
    });
    return () => cancelAnimationFrame(raf);
  }, [state.playing]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "SELECT" || t.tagName === "TEXTAREA") && (t as HTMLInputElement).type !== "range") return;
      const a = keyAction(e.key, e.shiftKey);
      if (a) {
        e.preventDefault();
        dispatch(a);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  return [state, dispatch];
}

interface BarProps {
  state: PlayerState;
  dispatch: (a: PlayerAction) => void;
  firstViolation: number | null;
  onJumpViolation: () => void;
  extra?: React.ReactNode;
}

/** PlayerBar holds the transport controls, the step slider and "jump to first violation". */
export function PlayerBar({ state, dispatch, firstViolation, onJumpViolation, extra }: BarProps) {
  return (
    <div className="player">
      <button title="back 100 (shift+←)" onClick={() => dispatch({ type: "step", delta: -100 })}>
        «100
      </button>
      <button title="back 1 (←)" onClick={() => dispatch({ type: "step", delta: -1 })}>
        ‹1
      </button>
      <button title="play/pause (space)" onClick={() => dispatch({ type: "toggle" })} style={{ minWidth: 64 }}>
        {state.playing ? "❚❚ pause" : "▶ play"}
      </button>
      <button title="forward 1 (→)" onClick={() => dispatch({ type: "step", delta: 1 })}>
        1›
      </button>
      <button title="forward 100 (shift+→)" onClick={() => dispatch({ type: "step", delta: 100 })}>
        100»
      </button>
      <label>
        speed{" "}
        <select value={state.speed} onChange={(e) => dispatch({ type: "speed", speed: Number(e.target.value) })}>
          {SPEEDS.map((s) => (
            <option key={s} value={s}>
              {s}×
            </option>
          ))}
        </select>
      </label>
      <input
        type="range"
        min={0}
        max={state.total}
        value={Math.round(state.step)}
        onChange={(e) => dispatch({ type: "seek", step: Number(e.target.value) })}
        style={{ flex: 1, minWidth: 120 }}
        aria-label="playhead"
      />
      <span className="step">
        step {Math.round(state.step)} / {state.total}
      </span>
      <button disabled={firstViolation === null} onClick={onJumpViolation} title="move the playhead to the first op a checker flagged">
        ⚑ jump to first violation
      </button>
      {extra}
    </div>
  );
}

/** ClusterStateBox shows node status, partition, clock offsets and leader at the playhead. */
export function ClusterStateBox({ model, step }: { model: RunModel; step: number }) {
  const cs = clusterStateAt(model, step);
  return (
    <div className="cluster-state">
      <h2>Cluster at step {Math.round(step)}</h2>
      <table>
        <thead>
          <tr>
            <th>node</th>
            <th>state</th>
            <th>clock</th>
          </tr>
        </thead>
        <tbody>
          {cs.nodes.map((n) => (
            <tr key={n.name}>
              <td>
                {n.name}
                {cs.leader === n.name ? " ★" : ""}
              </td>
              <td className={`state-${n.status}`}>{n.status}</td>
              <td>{n.offsetMs ? `${n.offsetMs > 0 ? "+" : ""}${n.offsetMs} ms` : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <div>partition: {cs.partition ? cs.partition.map((g) => g.join(",")).join(" | ") : "none"}</div>
      <div>leader: {cs.leader || <span className="note" style={{ padding: 0 }}>unknown (no leader notes in trace)</span>}</div>
    </div>
  );
}
