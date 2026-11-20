import { useEffect, useMemo, useState } from "react";
import { DEMO, listRuns, SAMPLE_ID, type RunSummary } from "../data/load";
import { formatUrl, minOriginal, runIdFromPath } from "../data/url";
import { EMPTY_FILTERS } from "./filters";
import { VerdictBadge } from "./Header";

const link = (run: string, compare: string | null = null) => "?" + formatUrl({ run, compare, step: null, viewport: null, filters: EMPTY_FILTERS });

/** RunList is the home page: every run from GET /api/runs with scenario/verdict filters. */
export function RunList() {
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [scenario, setScenario] = useState("");
  const [verdict, setVerdict] = useState("");
  useEffect(() => {
    listRuns()
      .then(setRuns)
      .catch((e: Error) => setError(e.message));
  }, []);
  const scenarios = useMemo(() => [...new Set((runs ?? []).map((r) => r.scenario))].sort(), [runs]);
  const verdicts = useMemo(() => [...new Set((runs ?? []).map((r) => r.verdict))].sort(), [runs]);
  const ids = useMemo(() => new Set((runs ?? []).map((r) => r.id)), [runs]);
  const shown = (runs ?? []).filter((r) => (!scenario || r.scenario === scenario) && (!verdict || r.verdict === verdict));

  return (
    <div className="runlist">
      <h1 style={{ fontSize: 18, marginTop: 0 }}>Kurukshetra runs</h1>
      {DEMO && (
        <p className="note" style={{ padding: 0, maxWidth: 760 }}>
          Live demo: real runs recorded with Docker, bundled as static files. Open a <b>FAIL</b> run, press <b>⚑ jump to first violation</b>, and read
          <b> Why it failed</b> on the right. Try <code>kuru</code> yourself: <a href="https://github.com/Yug912/Kurukshetra-A-Bug-Hunting-Simulator-for-Distributed-Systems">GitHub</a>.
        </p>
      )}
      {error && (
        <p className="error" style={{ padding: 0 }}>
          Could not reach the kuru API ({error}). Start <code>kuru serve</code>, or open the bundled <a href={link(SAMPLE_ID)}>sample run</a>.
        </p>
      )}
      {runs && (
        <>
          <div className="filters">
            <label>
              scenario{" "}
              <select value={scenario} onChange={(e) => setScenario(e.target.value)}>
                <option value="">all</option>
                {scenarios.map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </select>
            </label>
            <label>
              verdict{" "}
              <select value={verdict} onChange={(e) => setVerdict(e.target.value)}>
                <option value="">all</option>
                {verdicts.map((v) => (
                  <option key={v}>{v}</option>
                ))}
              </select>
            </label>
            <span className="note" style={{ padding: 0 }}>
              {shown.length} of {runs.length} runs · <a href={link(SAMPLE_ID)}>bundled sample</a>
            </span>
          </div>
          <table>
            <thead>
              <tr>
                <th>run</th>
                <th>scenario</th>
                <th>seed</th>
                <th>verdict</th>
                <th>faults</th>
                <th>fidelity</th>
                <th>started</th>
                <th>compare</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => {
                const orig = runIdFromPath(r.replay_of) ?? minOriginal(r.id);
                const min = ids.has(r.id + "-min") ? r.id + "-min" : null;
                return (
                  <tr key={r.id}>
                    <td>
                      <a href={link(r.id)}>{r.id}</a>
                    </td>
                    <td>{r.scenario}</td>
                    <td>{r.seed ?? "—"}</td>
                    <td>
                      <VerdictBadge verdict={r.verdict} />
                    </td>
                    <td>{r.faults}</td>
                    <td>{r.fidelity !== null && r.fidelity !== undefined ? `${(r.fidelity * 100).toFixed(1)}%` : "—"}</td>
                    <td>{r.started_at ? new Date(r.started_at).toLocaleString() : "—"}</td>
                    <td>
                      {orig && <a href={link(orig, r.id)}>vs original</a>} {min && <a href={link(r.id, min)}>vs minimal</a>}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {runs.length === 0 && <p className="note">No runs yet under the runs folder.</p>}
        </>
      )}
      {!runs && !error && <p className="note">Loading runs…</p>}
    </div>
  );
}
