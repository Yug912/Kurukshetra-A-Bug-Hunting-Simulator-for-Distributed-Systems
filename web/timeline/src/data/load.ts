// Loading run folders over HTTP: the static sample (public/sample-run) or `kuru serve`'s API.
import { parseJSONL } from "./jsonl";
import type { Fault, HistoryOp, Meta, RunData, RunResult, TraceEvent } from "./types";

/** The run id used for the bundled sample (?run=sample). */
export const SAMPLE_ID = "sample";

/**
 * DEMO is true in the static demo build (`VITE_DEMO=1 npm run build`): runs come from
 * demo/runs.json and demo/runs/<id>/ (written by `kuru serve --export`), with no kuru serve.
 */
export const DEMO = import.meta.env.VITE_DEMO === "1";

/** baseFor returns the URL prefix of a run's raw files. */
export function baseFor(id: string): string {
  if (id === SAMPLE_ID) return "sample-run/";
  const path = id.split("/").map(encodeURIComponent).join("/");
  return DEMO ? `demo/runs/${path}/` : `api/runs/${path}/`;
}

async function fetchText(url: string, optional = false): Promise<string | null> {
  const r = await fetch(url);
  if (!r.ok) {
    if (optional) return null;
    throw new Error(`${url}: HTTP ${r.status}`);
  }
  return r.text();
}

/** loadRun fetches every file of a run. result.json, faults.json and explain.md are optional. */
export async function loadRun(id: string): Promise<RunData> {
  const base = baseFor(id);
  const [meta, result, faults, trace, history, explain] = await Promise.all([
    fetchText(base + "meta.json"),
    fetchText(base + "result.json", true),
    fetchText(base + "faults.json", true),
    fetchText(base + "trace.jsonl", true),
    fetchText(base + "history.jsonl", true),
    fetchText(base + "explain.md", true),
  ]);
  return {
    id,
    base,
    meta: JSON.parse(meta!) as Meta,
    result: result ? (JSON.parse(result) as RunResult) : null,
    faults: faults ? ((JSON.parse(faults) as Fault[]) ?? []) : [],
    trace: trace ? parseJSONL<TraceEvent>(trace) : [],
    history: history ? parseJSONL<HistoryOp>(history) : [],
    explain,
  };
}

/** One entry of GET /api/runs. */
export interface RunSummary {
  id: string;
  scenario: string;
  seed: number | null;
  verdict: string;
  fidelity?: number | null;
  faults: number;
  started_at: string;
  replay_of?: string;
}

/** listRuns fetches GET /api/runs (or the demo build's static copy). */
export async function listRuns(): Promise<RunSummary[]> {
  const t = await fetchText(DEMO ? "demo/runs.json" : "api/runs");
  return JSON.parse(t!) as RunSummary[];
}
