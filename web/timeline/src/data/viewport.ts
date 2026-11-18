// Step-axis viewport math: zoom, pan and visible-range queries.

/** A visible step range [start, end]. */
export interface Viewport {
  start: number;
  end: number;
}

const MIN_SPAN = 10;

/** clamp keeps vp inside [0, total] and at least MIN_SPAN wide. */
export function clamp(vp: Viewport, total: number): Viewport {
  const maxSpan = Math.max(total, MIN_SPAN);
  let span = Math.min(Math.max(vp.end - vp.start, MIN_SPAN), maxSpan);
  let start = vp.start;
  if (start < 0) start = 0;
  if (start + span > maxSpan) start = maxSpan - span;
  if (start < 0) {
    start = 0;
    span = maxSpan;
  }
  return { start, end: start + span };
}

/** zoomAt scales the viewport by factor (< 1 zooms in) keeping anchor fixed on screen. */
export function zoomAt(vp: Viewport, factor: number, anchor: number, total: number): Viewport {
  const start = anchor - (anchor - vp.start) * factor;
  const end = anchor + (vp.end - anchor) * factor;
  return clamp({ start, end }, total);
}

/** pan shifts the viewport by delta steps. */
export function pan(vp: Viewport, delta: number, total: number): Viewport {
  return clamp({ start: vp.start + delta, end: vp.end + delta }, total);
}

/** centerOn moves the viewport so step is in the middle, keeping its span. */
export function centerOn(vp: Viewport, step: number, total: number): Viewport {
  const half = (vp.end - vp.start) / 2;
  return clamp({ start: step - half, end: step + half }, total);
}

/** Linear mapping from steps to pixels for a viewport and width. */
export function scaleX(vp: Viewport, width: number): (step: number) => number {
  const k = width / Math.max(vp.end - vp.start, 1);
  return (s) => (s - vp.start) * k;
}

/** lowerBound returns the first index i with key(items[i]) >= x (items sorted by key). */
export function lowerBound<T>(items: T[], x: number, key: (t: T) => number): number {
  let lo = 0;
  let hi = items.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (key(items[mid]) < x) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/**
 * visibleRange returns items (sorted by start) whose [start, end] overlaps [lo, hi].
 * maxSpan is the longest end-start of any item, so the scan can start at lo - maxSpan.
 */
export function visibleRange<T>(items: T[], lo: number, hi: number, start: (t: T) => number, end: (t: T) => number, maxSpan: number): T[] {
  const out: T[] = [];
  for (let i = lowerBound(items, lo - maxSpan, start); i < items.length; i++) {
    const it = items[i];
    if (start(it) > hi) break;
    if (end(it) >= lo) out.push(it);
  }
  return out;
}

/** decimate keeps at most max items, evenly spaced; returns the stride used. */
export function decimate<T>(items: T[], max: number): { items: T[]; stride: number } {
  if (items.length <= max) return { items, stride: 1 };
  const stride = Math.ceil(items.length / max);
  const out: T[] = [];
  for (let i = 0; i < items.length; i += stride) out.push(items[i]);
  return { items: out, stride };
}
