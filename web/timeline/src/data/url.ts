// URL state: ?run=...&step=...&filters=...&from=...&to=...&compare=...
import { decodeFilters, encodeFilters, type Filters } from "../view/filters";
import type { Viewport } from "./viewport";

export interface UrlState {
  run: string | null;
  compare: string | null;
  step: number | null;
  viewport: Viewport | null;
  filters: Filters;
}

function num(v: string | null): number | null {
  if (v === null || v === "") return null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
}

/** parseUrl reads the view state from a query string. */
export function parseUrl(search: string): UrlState {
  const q = new URLSearchParams(search);
  const from = num(q.get("from"));
  const to = num(q.get("to"));
  return {
    run: q.get("run") || null,
    compare: q.get("compare") || null,
    step: num(q.get("step")),
    viewport: from !== null && to !== null && to > from ? { start: from, end: to } : null,
    filters: decodeFilters(q.get("filters")),
  };
}

/** formatUrl renders view state as a query string (without the leading "?"); empty parts are left out. */
export function formatUrl(s: UrlState): string {
  const q = new URLSearchParams();
  if (s.run) q.set("run", s.run);
  if (s.compare) q.set("compare", s.compare);
  if (s.step !== null) q.set("step", String(Math.round(s.step)));
  if (s.viewport) {
    q.set("from", String(Math.round(s.viewport.start)));
    q.set("to", String(Math.round(s.viewport.end)));
  }
  const f = encodeFilters(s.filters);
  if (f) q.set("filters", f);
  return q.toString();
}

/**
 * runIdFromPath turns a replay_of / faults_from style path ("runs/toykv/6", "/abs/runs/toykv/6/")
 * into a run id ("toykv/6"): its last two segments.
 */
export function runIdFromPath(p: string | undefined): string | null {
  if (!p) return null;
  const parts = p.split(/[\\/]+/).filter(Boolean);
  return parts.length >= 2 ? parts.slice(-2).join("/") : null;
}

/** minOriginal returns the original run id of a shrink result ("x/7-min" → "x/7"), else null. */
export function minOriginal(id: string): string | null {
  return id.endsWith("-min") ? id.slice(0, -4) : null;
}
