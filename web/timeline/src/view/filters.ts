// Filter state for the timeline, and its (de)serialisation for the URL.

/** Event kinds that can be toggled. */
export const KINDS = ["deliver", "drop", "delay", "dup", "faults", "ops"] as const;
export type FilterKind = (typeof KINDS)[number];

export interface Filters {
  hiddenNodes: string[];
  hiddenKinds: FilterKind[];
  hiddenClients: number[];
  hiddenPaths: string[];
}

export const EMPTY_FILTERS: Filters = { hiddenNodes: [], hiddenKinds: [], hiddenClients: [], hiddenPaths: [] };

/** toggle adds or removes v from list. */
export function toggle<T>(list: T[], v: T): T[] {
  return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
}

/**
 * encodeFilters renders filters as a compact string: "n:n2,n3;k:drop;c:1;p:/peer/append_entries".
 * Empty filters encode to "".
 */
export function encodeFilters(f: Filters): string {
  const parts: string[] = [];
  if (f.hiddenNodes.length) parts.push("n:" + f.hiddenNodes.join(","));
  if (f.hiddenKinds.length) parts.push("k:" + f.hiddenKinds.join(","));
  if (f.hiddenClients.length) parts.push("c:" + f.hiddenClients.join(","));
  if (f.hiddenPaths.length) parts.push("p:" + f.hiddenPaths.join(","));
  return parts.join(";");
}

/** decodeFilters is the inverse of encodeFilters; unknown parts are ignored. */
export function decodeFilters(s: string | null): Filters {
  const f: Filters = { hiddenNodes: [], hiddenKinds: [], hiddenClients: [], hiddenPaths: [] };
  if (!s) return f;
  for (const part of s.split(";")) {
    const i = part.indexOf(":");
    if (i < 0) continue;
    const vals = part.slice(i + 1).split(",").filter(Boolean);
    switch (part.slice(0, i)) {
      case "n":
        f.hiddenNodes = vals;
        break;
      case "k":
        f.hiddenKinds = vals.filter((v): v is FilterKind => (KINDS as readonly string[]).includes(v));
        break;
      case "c":
        f.hiddenClients = vals.map(Number).filter((n) => Number.isInteger(n));
        break;
      case "p":
        f.hiddenPaths = vals;
        break;
    }
  }
  return f;
}
