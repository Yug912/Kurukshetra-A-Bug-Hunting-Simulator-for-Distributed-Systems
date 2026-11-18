// Typed mirrors of the Go run-folder formats (pkg/trace, pkg/history, pkg/core).

export type EventKind =
  | "run_start"
  | "run_end"
  | "msg_send"
  | "msg_deliver"
  | "msg_drop"
  | "msg_delay"
  | "msg_dup"
  | "partition"
  | "heal"
  | "crash"
  | "restart"
  | "pause"
  | "resume"
  | "clock_skew"
  | "note";

/** One line of trace.jsonl (trace.Event). */
export interface TraceEvent {
  seq: number;
  step: number;
  wall_ns: number;
  kind: EventKind;
  node?: string;
  from?: string;
  to?: string;
  msg_id?: string;
  size?: number;
  meta?: Record<string, string>;
}

export type OpType = "invoke" | "ok" | "fail" | "info";
export type OpF = "read" | "write" | "cas";

/** One line of history.jsonl (history.Op). */
export interface HistoryOp {
  index: number;
  client: number;
  type: OpType;
  f: OpF;
  key: string;
  value?: string;
  expect?: string;
  node: string;
  step: number;
  wall_ns: number;
  final?: boolean;
  error?: string;
}

/** core.CheckResult */
export interface CheckResult {
  checker: string;
  verdict: string;
  reason?: string;
  bad_ops?: number[];
  artifacts?: Record<string, string>;
}

/** core.Result (result.json) */
export interface RunResult {
  seed: number;
  scenario: string;
  verdict: string;
  error?: string;
  checks: CheckResult[];
  fidelity?: number;
}

/** core.Fault (faults.json entries) */
export interface Fault {
  step: number;
  kind: string;
  nodes?: string[];
  groups?: string[][];
  duration_steps: number;
  offset_ms?: number;
}

/** core.Meta (meta.json) */
export interface Meta {
  scenario: string;
  seed: number;
  nodes: string[];
  mode: string;
  runner: string;
  started_at: string;
  duration_ms: number;
  steps: number;
  replay_of?: string;
  faults_from?: string;
  version: string;
}

/** Everything the viewer loads for one run. */
export interface RunData {
  id: string;
  base: string; // URL prefix for raw files, ending in "/"
  meta: Meta;
  result: RunResult | null;
  faults: Fault[];
  trace: TraceEvent[];
  history: HistoryOp[];
}
