import { useMemo, useRef } from "react";
import type { RunModel } from "../data/model";
import { centerOn, type Viewport } from "../data/viewport";
import { GUTTER } from "./Timeline";

const H = 44;
const BINS = 400;

/** Minimap shows message density, faults and bad ops over the whole run, plus the viewport. */
export function Minimap({ model, vp, width, playhead, onViewport }: { model: RunModel; vp: Viewport; width: number; playhead?: number; onViewport: (vp: Viewport) => void }) {
  const ref = useRef<SVGSVGElement>(null);
  const plotW = Math.max(width - GUTTER, 50);
  const total = model.totalSteps;
  const x = (s: number) => GUTTER + (s / total) * plotW;

  const density = useMemo(() => {
    const bins = new Array<number>(BINS).fill(0);
    for (const m of model.messages) bins[Math.min(BINS - 1, Math.floor((m.send / total) * BINS))]++;
    const max = Math.max(...bins, 1);
    return bins.map((b) => b / max);
  }, [model, total]);

  const moveTo = (clientX: number) => {
    const rect = ref.current!.getBoundingClientRect();
    const step = ((clientX - rect.left - GUTTER) / plotW) * total;
    onViewport(centerOn(vp, step, total));
  };

  return (
    <div className="minimap">
      <svg
        ref={ref}
        width={width}
        height={H}
        onMouseDown={(e) => moveTo(e.clientX)}
        onMouseMove={(e) => e.buttons === 1 && moveTo(e.clientX)}
      >
        <text x={10} y={26} fill="var(--muted)" fontSize={11}>
          overview
        </text>
        {density.map((d, i) => (
          <rect key={i} x={GUTTER + (i / BINS) * plotW} y={H - 6 - d * 26} width={plotW / BINS + 0.5} height={d * 26} fill="var(--msg)" opacity={0.45} />
        ))}
        {model.partitions.map((iv, i) => (
          <rect key={`p${i}`} x={x(iv.start)} y={4} width={Math.max(x(iv.end) - x(iv.start), 1)} height={H - 8} fill="var(--partition)" />
        ))}
        {model.down.map((iv, i) => (
          <rect key={`d${i}`} x={x(iv.start)} y={H - 6} width={Math.max(x(iv.end) - x(iv.start), 1)} height={4} fill="var(--down)" />
        ))}
        {model.ops
          .filter((p) => p.bad)
          .map((p) => (
            <line key={p.invoke.index} x1={x(p.start)} x2={x(p.start)} y1={2} y2={H - 2} stroke="var(--bad)" strokeWidth={2} />
          ))}
        <rect x={x(vp.start)} y={1} width={Math.max(x(vp.end) - x(vp.start), 2)} height={H - 2} fill="none" stroke="var(--accent)" strokeWidth={1.5} />
        {playhead !== undefined && <line x1={x(playhead)} x2={x(playhead)} y1={0} y2={H} stroke="var(--playhead)" strokeWidth={1.5} />}
      </svg>
    </div>
  );
}
