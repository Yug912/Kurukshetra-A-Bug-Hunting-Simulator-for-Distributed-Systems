import { useEffect, useState } from "react";
import { loadRun } from "./data/load";
import { buildModel, type RunModel } from "./data/model";
import { syntheticRun } from "./data/synthetic";
import { RunView } from "./view/RunView";

/** useModel loads and derives one run by id ("sample", "synthetic" or "<scenario>/<run>"). */
export function useModel(id: string | null): { model: RunModel | null; error: string | null } {
  const [state, setState] = useState<{ model: RunModel | null; error: string | null }>({ model: null, error: null });
  useEffect(() => {
    if (!id) return;
    let live = true;
    setState({ model: null, error: null });
    const load = id === "synthetic" ? Promise.resolve(syntheticRun(50_000)) : loadRun(id);
    load
      .then((run) => live && setState({ model: buildModel(run), error: null }))
      .catch((e: Error) => live && setState({ model: null, error: `Could not load run ${id}: ${e.message}` }));
    return () => {
      live = false;
    };
  }, [id]);
  return state;
}

export function App() {
  const id = new URLSearchParams(location.search).get("run");
  const { model, error } = useModel(id);
  if (!id) {
    return (
      <div className="error" style={{ color: "inherit" }}>
        Open a run with <code>?run=sample</code> (bundled sample), <code>?run=synthetic</code> (50,000 events) or <code>?run=&lt;scenario&gt;/&lt;run&gt;</code> via <code>kuru serve</code>.
      </div>
    );
  }
  if (error) return <div className="error">{error}</div>;
  if (!model) return <div className="note">Loading {id}…</div>;
  return (
    <div className="app">
      <RunView model={model} />
    </div>
  );
}
