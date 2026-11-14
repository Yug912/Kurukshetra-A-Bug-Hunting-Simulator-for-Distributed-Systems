import { useCallback, useEffect, useMemo, useState } from "react";
import type { RunModel } from "../data/model";
import { formatUrl, type UrlState } from "../data/url";
import { centerOn, clamp, type Viewport } from "../data/viewport";
import type { Filters } from "./filters";
import { ClusterStateBox, PlayerBar, usePlayer } from "./Player";
import { RunView } from "./RunView";
import { Toolbar } from "./Toolbar";

interface Props {
  model: RunModel;
  compare: RunModel | null;
  initial: UrlState;
}

/** RunPage owns player, viewport and filter state for one run (or two in compare mode) and mirrors it into the URL. */
export function RunPage({ model, compare, initial }: Props) {
  const total = Math.max(model.totalSteps, compare?.totalSteps ?? 0);
  const [player, dispatch] = usePlayer(total, initial.step ?? 0);
  const [vp, setVp] = useState<Viewport>(() => clamp(initial.viewport ?? { start: 0, end: total }, total));
  const [filters, setFilters] = useState<Filters>(initial.filters);
  const onViewport = useCallback((v: Viewport) => setVp(v), []);

  // Keep the URL in sync (throttled while playing).
  useEffect(() => {
    const t = setTimeout(
      () => {
        const q = formatUrl({ run: model.run.id, compare: compare?.run.id ?? null, step: player.step, viewport: vp, filters });
        history.replaceState(null, "", "?" + q);
      },
      player.playing ? 400 : 50,
    );
    return () => clearTimeout(t);
  }, [model, compare, player.step, player.playing, vp, filters]);

  // While playing, keep the playhead in view.
  useEffect(() => {
    if (player.playing && (player.step > vp.end || player.step < vp.start)) setVp((v) => clamp({ start: player.step - (v.end - v.start) * 0.1, end: player.step + (v.end - v.start) * 0.9 }, total));
  }, [player.step, player.playing, vp, total]);

  const violation = useMemo(() => {
    const steps = [model.firstViolation, compare?.firstViolation ?? null].filter((s): s is number => s !== null);
    return steps.length ? Math.min(...steps) : null;
  }, [model, compare]);
  const jump = () => {
    if (violation === null) return;
    dispatch({ type: "pause" });
    dispatch({ type: "seek", step: violation });
    setVp((v) => centerOn(v, violation, total));
  };
  const home = <a href="?">all runs</a>;
  const bar = <PlayerBar state={player} dispatch={dispatch} firstViolation={violation} onJumpViolation={jump} extra={home} />;

  if (!compare) {
    return (
      <div className="app">
        <RunView
          model={model}
          vp={vp}
          onViewport={onViewport}
          filters={filters}
          onFilters={setFilters}
          playhead={player.step}
          above={bar}
          side={<ClusterStateBox model={model} step={player.step} />}
        />
      </div>
    );
  }
  return (
    <div className="app">
      <Toolbar model={model} filters={filters} onChange={setFilters} />
      {bar}
      <div className="compare">
        {[model, compare].map((m, i) => (
          <div className="pane" key={i}>
            <div className="pane-title">{i === 0 ? "original" : m.run.id.endsWith("-min") ? "minimal (shrunk)" : "replay / comparison"}</div>
            <RunView model={m} vp={vp} onViewport={onViewport} filters={filters} onFilters={setFilters} playhead={player.step} compact />
          </div>
        ))}
      </div>
    </div>
  );
}
