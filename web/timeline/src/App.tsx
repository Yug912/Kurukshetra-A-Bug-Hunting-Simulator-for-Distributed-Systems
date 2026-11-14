import { useEffect, useState } from "react";
import { loadRun } from "./data/load";
import { buildModel, type RunModel } from "./data/model";
import { syntheticRun } from "./data/synthetic";
import { parseUrl } from "./data/url";
import { RunList } from "./view/RunList";
import { RunPage } from "./view/RunPage";

async function load(id: string): Promise<RunModel> {
  const run = id === "synthetic" ? syntheticRun(50_000) : await loadRun(id);
  return buildModel(run);
}

/** useModels loads one or two runs by id ("sample", "synthetic" or "<scenario>/<run>"). */
function useModels(ids: (string | null)[]): { models: (RunModel | null)[]; error: string | null; loading: boolean } {
  const key = ids.join("|");
  const [state, setState] = useState<{ models: (RunModel | null)[]; error: string | null; loading: boolean }>({ models: [], error: null, loading: true });
  useEffect(() => {
    let live = true;
    setState({ models: [], error: null, loading: true });
    Promise.all(ids.map((id) => (id ? load(id).catch((e: Error) => Promise.reject(new Error(`run ${id}: ${e.message}`))) : Promise.resolve(null))))
      .then((models) => live && setState({ models, error: null, loading: false }))
      .catch((e: Error) => live && setState({ models: [], error: `Could not load ${e.message}`, loading: false }));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return state;
}

export function App() {
  const initial = parseUrl(location.search);
  if (!initial.run) return <RunList />;
  return <RunRoute initial={initial} />;
}

function RunRoute({ initial }: { initial: ReturnType<typeof parseUrl> }) {
  const { models, error, loading } = useModels([initial.run, initial.compare]);
  if (error)
    return (
      <div className="error">
        {error}. <a href="?">All runs</a> · <a href="?run=sample">sample</a>
      </div>
    );
  if (loading || !models[0]) return <div className="note">Loading {initial.run}…</div>;
  return <RunPage model={models[0]} compare={models[1] ?? null} initial={initial} />;
}
