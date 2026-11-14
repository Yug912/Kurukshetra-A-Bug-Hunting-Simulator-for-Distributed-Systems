import { useEffect, useMemo, useRef, useState } from "react";
import { opLabel, opOutcome, type Interval, type Message, type OpPair, type SkewMarker } from "../data/derive";
import type { RunModel } from "../data/model";
import { decimate, pan, scaleX, visibleRange, zoomAt, type Viewport } from "../data/viewport";
import type { Filters } from "./filters";

/** What the user clicked. */
export type Selection =
  | { kind: "message"; msg: Message }
  | { kind: "op"; op: OpPair }
  | { kind: "interval"; what: "partition" | "crash" | "pause"; iv: Interval }
  | { kind: "skew"; skew: SkewMarker }
  | null;

export const GUTTER = 64;
const AXIS_H = 22;
const LANE_H = 44;
const CLIENT_H = 26;
const GAP = 22;
const MAX_PLAIN_MESSAGES = 1500;
const MAX_SPECIAL_MESSAGES = 600;

interface Props {
  model: RunModel;
  vp: Viewport;
  width: number;
  filters: Filters;
  selection: Selection;
  playhead?: number;
  onSelect: (s: Selection) => void;
  onViewport: (vp: Viewport) => void;
}

/** niceStep returns a round tick spacing close to raw (1, 2, 5 × 10^n). */
export function niceStep(raw: number): number {
  const p = Math.pow(10, Math.floor(Math.log10(Math.max(raw, 1e-9))));
  for (const m of [1, 2, 5, 10]) if (m * p >= raw) return m * p;
  return 10 * p;
}

/** Timeline draws node lanes (messages, faults) and client lanes (ops) for the viewport. */
export function Timeline({ model, vp, width, filters, selection, playhead, onSelect, onViewport }: Props) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [drag, setDrag] = useState<{ x: number; vp: Viewport; moved: boolean } | null>(null);
  const plotW = Math.max(width - GUTTER, 50);
  const x = useMemo(() => {
    const s = scaleX(vp, plotW);
    return (step: number) => GUTTER + s(step);
  }, [vp, plotW]);

  const nodes = model.nodes.filter((n) => !filters.hiddenNodes.includes(n));
  const clients = filters.hiddenKinds.includes("ops") ? [] : model.clients.filter((c) => !filters.hiddenClients.includes(c));
  const nodeY = new Map(nodes.map((n, i) => [n, AXIS_H + i * LANE_H + LANE_H / 2]));
  const nodesBottom = AXIS_H + nodes.length * LANE_H;
  const clientTop = nodesBottom + GAP;
  const clientY = new Map(clients.map((c, i) => [c, clientTop + i * CLIENT_H + CLIENT_H / 2]));
  const height = clientTop + clients.length * CLIENT_H + 12;
  const hidden = (k: string) => filters.hiddenKinds.includes(k as never);
  const dim = (step: number) => (playhead !== undefined && step > playhead ? 0.22 : 1);

  // Wheel zoom (non-passive so the page does not scroll).
  useEffect(() => {
    const el = svgRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const rect = el.getBoundingClientRect();
      const px = e.clientX - rect.left - GUTTER;
      if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) {
        onViewport(pan(vp, (e.deltaX / plotW) * (vp.end - vp.start), model.totalSteps));
        return;
      }
      const anchor = vp.start + (px / plotW) * (vp.end - vp.start);
      onViewport(zoomAt(vp, Math.exp(e.deltaY * 0.0015), anchor, model.totalSteps));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [vp, plotW, model.totalSteps, onViewport]);

  const lo = vp.start;
  const hi = vp.end;
  const msgs = useMemo(() => {
    const vis = visibleRange(model.messages, lo, hi, (m) => m.send, (m) => m.end, model.maxMsgSpan).filter(
      (m) => nodeY.has(m.from) && nodeY.has(m.to) && !filters.hiddenPaths.includes(m.path),
    );
    const special = vis.filter((m) => m.drop !== null || m.delaySteps > 0 || m.dup);
    const plain = vis.filter((m) => !(m.drop !== null || m.delaySteps > 0 || m.dup));
    const d = decimate(plain, MAX_PLAIN_MESSAGES);
    const ds = decimate(special, MAX_SPECIAL_MESSAGES);
    return { special: ds.items, specialStride: ds.stride, plain: d.items, stride: d.stride, total: vis.length };
  }, [model, lo, hi, filters, nodes.join(",")]);
  const ops = useMemo(
    () => visibleRange(model.ops, lo, hi, (p) => p.start, (p) => p.end ?? model.totalSteps, model.maxOpSpan),
    [model, lo, hi],
  );

  const ticks = useMemo(() => {
    const step = niceStep(((hi - lo) / plotW) * 90);
    const out: number[] = [];
    for (let t = Math.ceil(lo / step) * step; t <= hi; t += step) out.push(t);
    return out;
  }, [lo, hi, plotW]);

  const isSel = (s: Selection) => JSON.stringify(s) === JSON.stringify(selection);
  const clipL = (s: number) => Math.max(x(s), GUTTER - 2);
  const clipR = (s: number) => Math.min(x(s), GUTTER + plotW + 2);

  const onMouseDown = (e: React.MouseEvent) => setDrag({ x: e.clientX, vp, moved: false });
  const onMouseMove = (e: React.MouseEvent) => {
    if (!drag) return;
    const dx = e.clientX - drag.x;
    if (Math.abs(dx) > 3) {
      onViewport(pan(drag.vp, (-dx / plotW) * (drag.vp.end - drag.vp.start), model.totalSteps));
      if (!drag.moved) setDrag({ ...drag, moved: true });
    }
  };
  const endDrag = () => setDrag(null);
  const click = (s: Selection) => (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!drag?.moved) onSelect(s);
  };

  const drawMessage = (m: Message) => {
    const y1 = nodeY.get(m.from)!;
    const y2 = nodeY.get(m.to)!;
    const sel = selection?.kind === "message" && selection.msg.id === m.id;
    const op = dim(m.send);
    const out: React.ReactNode[] = [];
    if (m.drop !== null) {
      if (hidden("drop")) return null;
      const xe = x(m.drop);
      const ye = (y1 + y2) / 2;
      out.push(
        <line key="l" x1={x(m.send)} y1={y1} x2={xe} y2={ye} stroke="var(--drop)" strokeWidth={sel ? 2.5 : 1.2} />,
        <path key="x" d={`M${xe - 4},${ye - 4}L${xe + 4},${ye + 4}M${xe - 4},${ye + 4}L${xe + 4},${ye - 4}`} stroke="var(--drop)" strokeWidth={2} />,
      );
    } else {
      const kind = m.delaySteps > 0 ? "delay" : m.dup ? "dup" : "deliver";
      if (hidden(kind) || (m.dup && hidden("dup"))) return null;
      const color = m.delaySteps > 0 ? "var(--delay)" : "var(--msg)";
      for (const [i, d] of m.deliveries.entries()) {
        out.push(
          <line
            key={i}
            x1={x(m.send)}
            y1={y1}
            x2={x(d)}
            y2={y2}
            stroke={sel ? "var(--select)" : color}
            strokeWidth={sel ? 2.5 : m.delaySteps > 0 || m.dup ? 1.6 : 1}
            strokeDasharray={m.dup ? "4 3" : undefined}
            markerEnd={`url(#arrow-${m.delaySteps > 0 ? "delay" : "msg"})`}
          />,
        );
      }
    }
    return (
      <g key={m.id} className="clickable" opacity={op} onClick={click({ kind: "message", msg: m })}>
        <title>{`${m.from}→${m.to} ${m.path} (${m.id})`}</title>
        {/* wide invisible hit target */}
        <line x1={x(m.send)} y1={y1} x2={x(m.deliveries[0] ?? m.drop ?? m.end)} y2={m.drop !== null ? (y1 + y2) / 2 : y2} stroke="transparent" strokeWidth={8} />
        {out}
      </g>
    );
  };

  return (
    <div>
      <svg
        ref={svgRef}
        width={width}
        height={height}
        className={drag?.moved ? "dragging" : undefined}
        onMouseDown={onMouseDown}
        onMouseMove={onMouseMove}
        onMouseUp={endDrag}
        onMouseLeave={endDrag}
        onClick={() => !drag?.moved && onSelect(null)}
      >
        <defs>
          <marker id="arrow-msg" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M0,0L10,5L0,10z" fill="var(--msg)" />
          </marker>
          <marker id="arrow-delay" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M0,0L10,5L0,10z" fill="var(--delay)" />
          </marker>
          <pattern id="hatch" width="6" height="6" patternTransform="rotate(45)" patternUnits="userSpaceOnUse">
            <line x1="0" y1="0" x2="0" y2="6" stroke="var(--down)" strokeWidth="2" />
          </pattern>
          <clipPath id="plot">
            <rect x={GUTTER} y={0} width={plotW} height={height} />
          </clipPath>
        </defs>

        <g className="axis">
          {ticks.map((t) => (
            <g key={t}>
              <line x1={x(t)} x2={x(t)} y1={AXIS_H - 4} y2={height} strokeDasharray="2 4" />
              <text x={x(t) + 2} y={12}>
                {t}
              </text>
            </g>
          ))}
        </g>

        <g clipPath="url(#plot)">
          {!hidden("faults") &&
            model.partitions
              .filter((iv) => iv.end >= lo && iv.start <= hi)
              .map((iv, i) => (
                <g key={`p${i}`} className="clickable" opacity={dim(iv.start)} onClick={click({ kind: "interval", what: "partition", iv })}>
                  <rect x={clipL(iv.start)} y={AXIS_H} width={Math.max(clipR(iv.end) - clipL(iv.start), 1)} height={nodesBottom - AXIS_H} fill="var(--partition)" stroke={isSel({ kind: "interval", what: "partition", iv }) ? "var(--select)" : "none"} />
                  {clipR(iv.end) - clipL(iv.start) > 90 && (
                    <text x={clipL(iv.start) + 4} y={AXIS_H + 11} fill="var(--partition-text)" fontSize={11} fontWeight={600}>
                      partition {iv.label.replace(/\|/g, " | ")}
                    </text>
                  )}
                </g>
              ))}

          {nodes.map((n) => {
            const y = nodeY.get(n)!;
            return <line key={n} x1={GUTTER} x2={GUTTER + plotW} y1={y} y2={y} stroke="var(--lane)" strokeWidth={2} />;
          })}

          {!hidden("faults") &&
            model.down
              .filter((iv) => nodeY.has(iv.node!) && iv.end >= lo && iv.start <= hi)
              .map((iv, i) => {
                const y = nodeY.get(iv.node!)!;
                return (
                  <g key={`d${i}`} className="clickable" opacity={dim(iv.start)} onClick={click({ kind: "interval", what: "crash", iv })}>
                    <title>{`crashed ${iv.node} ${iv.start}–${iv.end}`}</title>
                    <rect x={clipL(iv.start)} y={y - 9} width={Math.max(clipR(iv.end) - clipL(iv.start), 2)} height={18} fill="var(--bg)" />
                    <rect x={clipL(iv.start)} y={y - 9} width={Math.max(clipR(iv.end) - clipL(iv.start), 2)} height={18} fill="url(#hatch)" stroke="var(--down)" />
                    <text x={clipL(iv.start) + 3} y={y - 12} fill="var(--down)" fontSize={10} fontWeight={600}>
                      crash {iv.node}
                    </text>
                  </g>
                );
              })}

          {!hidden("faults") &&
            model.paused
              .filter((iv) => nodeY.has(iv.node!) && iv.end >= lo && iv.start <= hi)
              .map((iv, i) => {
                const y = nodeY.get(iv.node!)!;
                return (
                  <g key={`z${i}`} className="clickable" opacity={dim(iv.start)} onClick={click({ kind: "interval", what: "pause", iv })}>
                    <title>{`paused ${iv.node} ${iv.start}–${iv.end}`}</title>
                    <rect x={clipL(iv.start)} y={y - 6} width={Math.max(clipR(iv.end) - clipL(iv.start), 2)} height={12} fill="var(--paused)" rx={3} />
                    <text x={clipL(iv.start) + 3} y={y - 9} fill="var(--muted)" fontSize={10}>
                      pause
                    </text>
                  </g>
                );
              })}

          {msgs.plain.map(drawMessage)}
          {msgs.special.map(drawMessage)}

          {!hidden("faults") &&
            model.skews
              .filter((s) => nodeY.has(s.node) && s.step >= lo && s.step <= hi)
              .map((s, i) => {
                const y = nodeY.get(s.node)! + 12;
                const label = `${s.offsetMs >= 0 ? "+" : ""}${s.offsetMs}ms`;
                return (
                  <g key={`s${i}`} className="clickable" opacity={dim(s.step)} onClick={click({ kind: "skew", skew: s })}>
                    <rect x={x(s.step) - 2} y={y} width={label.length * 6 + 10} height={14} rx={7} fill="var(--info)" />
                    <text x={x(s.step) + 3} y={y + 10.5} fill="#fff" fontSize={10} fontWeight={600}>
                      ⏱{label}
                    </text>
                  </g>
                );
              })}

          {ops
            .filter((p) => clientY.has(p.client))
            .map((p) => {
              const y = clientY.get(p.client)!;
              const x1 = clipL(p.start);
              const x2 = clipR(p.end ?? model.totalSteps);
              const w = Math.max(x2 - x1, 3);
              const out = opOutcome(p);
              const fill = out === "ok" ? "var(--ok)" : out === "fail" ? "var(--fail)" : "var(--info)";
              const sel = selection?.kind === "op" && selection.op.invoke.index === p.invoke.index;
              const final = p.invoke.final;
              return (
                <g key={p.invoke.index} className="clickable" opacity={dim(p.start)} onClick={click({ kind: "op", op: p })}>
                  <title>{`${opLabel(p)} on ${p.invoke.node} (${out}${p.complete?.error ? ": " + p.complete.error : ""})`}</title>
                  <rect
                    x={x1}
                    y={y - 8}
                    width={w}
                    height={16}
                    rx={3}
                    fill={fill}
                    fillOpacity={final ? 0.25 : 0.55}
                    stroke={p.bad ? "var(--bad)" : sel ? "var(--select)" : final ? "var(--final)" : fill}
                    strokeWidth={p.bad ? 3 : sel ? 2 : 1}
                    strokeDasharray={final && !p.bad ? "3 2" : undefined}
                  />
                  {w > 48 && (
                    <text className="op-label" x={x1 + 4} y={y + 3.5}>
                      {opLabel(p)}
                    </text>
                  )}
                </g>
              );
            })}

          {playhead !== undefined && playhead >= lo && playhead <= hi && (
            <line x1={x(playhead)} x2={x(playhead)} y1={AXIS_H - 6} y2={height} stroke="var(--playhead)" strokeWidth={2} />
          )}
        </g>

        <rect x={0} y={0} width={GUTTER - 4} height={height} fill="var(--bg)" />
        {nodes.map((n) => (
          <text key={n} className="lane-label" x={10} y={nodeY.get(n)! + 4}>
            {n}
          </text>
        ))}
        {clients.length > 0 && (
          <text className="client-label" x={10} y={clientTop - 6}>
            clients
          </text>
        )}
        {clients.map((c) => (
          <text key={c} className="client-label" x={10} y={clientY.get(c)! + 4}>
            c{c}
          </text>
        ))}
      </svg>
      {(msgs.stride > 1 || msgs.specialStride > 1) && (
        <div className="note">
          {msgs.total} messages in view: showing 1 in {msgs.stride} ordinary messages
          {msgs.specialStride > 1 ? ` and 1 in ${msgs.specialStride} drops/delays/duplicates` : " and every drop/delay/duplicate"}. Zoom in to see all.
        </div>
      )}
    </div>
  );
}
