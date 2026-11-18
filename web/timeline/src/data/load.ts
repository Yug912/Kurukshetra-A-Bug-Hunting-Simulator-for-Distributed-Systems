// Loading run folders over HTTP: the static sample (public/sample-run) or `kuru serve`'s API.
import { parseJSONL } from "./jsonl";
import type { Fault, HistoryOp, Meta, RunData, RunResult, TraceEvent } from "./types";

/** The run id used for the bundled sample (?run=sample). */
export const SAMPLE_ID = "sample";

/** baseFor returns the URL prefix of a run's raw files. */
export function baseFor(id: string): string {
  if (id === SAMPLE_ID) return "sample-run/";
  return `api/runs/${id.split("/").map(encodeURIComponent).join("/")}/`;
}

async function fetchText(url: string, optional = false): Promise<string | null> {
  const r = await fetch(url);
  if (!r.ok) {
    if (optional) return null;
    throw new Error(`${url}: HTTP ${r.status}`);
  }
  return r.text();
}

/** loadRun fetches every file of a run. result.json and faults.json are optional. */
export async function loadRun(id: string): Promise<RunData> {
  const base = baseFor(id);
  const [meta, result, faults, trace, history] = await Promise.all([
    fetchText(base + "meta.json"),
    fetchText(base + "result.json", true),
    fetchText(base + "faults.json", true),
    fetchText(base + "trace.jsonl", true),
    fetchText(base + "history.jsonl", true),
  ]);
  return {
    id,
    base,
    meta: JSON.parse(meta!) as Meta,
    result: result ? (JSON.parse(result) as RunResult) : null,
    faults: faults ? ((JSON.parse(faults) as Fault[]) ?? []) : [],
    trace: trace ? parseJSONL<TraceEvent>(trace) : [],
    history: history ? parseJSONL<HistoryOp>(history) : [],
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

/** listRuns fetches GET /api/runs. */
export async function listRuns(): Promise<RunSummary[]> {
  const t = await fetchText("api/runs");
  return JSON.parse(t!) as RunSummary[];
}
