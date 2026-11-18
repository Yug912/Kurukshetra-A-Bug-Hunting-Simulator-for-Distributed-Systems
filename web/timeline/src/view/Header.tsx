import type { RunModel } from "../data/model";

export function VerdictBadge({ verdict }: { verdict: string }) {
  const cls = verdict === "PASS" || verdict === "FAIL" ? verdict : "other";
  return <span className={`badge ${cls}`}>{verdict || "?"}</span>;
}

const RAW_FILES = ["meta.json", "result.json", "faults.json", "scenario.yaml", "trace.jsonl", "history.jsonl"];

/** Header shows the run's identity, verdict and links to its raw files. */
export function Header({ model, extra }: { model: RunModel; extra?: React.ReactNode }) {
  const { run } = model;
  const r = run.result;
  const artifacts = new Set<string>();
  for (const c of r?.checks ?? []) for (const f of Object.values(c.artifacts ?? {})) artifacts.add(f);
  return (
    <div className="header">
      <h1>{run.meta.scenario}</h1>
      <span className="meta">seed {run.meta.seed}</span>
      <VerdictBadge verdict={r?.verdict ?? "—"} />
      {r?.fidelity !== undefined && r?.fidelity !== null && <span className="meta">fidelity {(r.fidelity * 100).toFixed(1)}%</span>}
      <span className="meta">{run.faults.length} faults</span>
      <span className="meta">
        {run.trace.length} events · {run.history.length} ops · {model.totalSteps} steps
      </span>
      {run.meta.replay_of && <span className="meta">replay of {run.meta.replay_of}</span>}
      {r?.checks?.map((c) => (
        <span key={c.checker} className="meta" title={c.reason}>
          {c.checker}: <VerdictBadge verdict={c.verdict} />
          {c.reason ? ` ${c.reason}` : ""}
        </span>
      ))}
      {run.base && (
        <span className="links">
          {RAW_FILES.map((f) => (
            <a key={f} href={run.base + f} target="_blank" rel="noreferrer">
              {f}
            </a>
          ))}
          {[...artifacts].map((f) => (
            <a key={f} href={run.base + f} target="_blank" rel="noreferrer">
              {f}
            </a>
          ))}
        </span>
      )}
      {extra}
    </div>
  );
}
