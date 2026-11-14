import type { RunModel } from "../data/model";
import { KINDS, toggle, type Filters } from "./filters";

interface Props {
  model: RunModel;
  filters: Filters;
  onChange: (f: Filters) => void;
}

/** Toolbar holds the filter checkboxes. */
export function Toolbar({ model, filters, onChange }: Props) {
  return (
    <div className="toolbar">
      <div className="group">
        <span>nodes</span>
        {model.nodes.map((n) => (
          <label key={n}>
            <input type="checkbox" checked={!filters.hiddenNodes.includes(n)} onChange={() => onChange({ ...filters, hiddenNodes: toggle(filters.hiddenNodes, n) })} />
            {n}
          </label>
        ))}
      </div>
      <div className="group">
        <span>show</span>
        {KINDS.map((k) => (
          <label key={k}>
            <input type="checkbox" checked={!filters.hiddenKinds.includes(k)} onChange={() => onChange({ ...filters, hiddenKinds: toggle(filters.hiddenKinds, k) })} />
            {k}
          </label>
        ))}
      </div>
      {model.clients.length > 0 && (
        <div className="group">
          <span>clients</span>
          {model.clients.map((c) => (
            <label key={c}>
              <input type="checkbox" checked={!filters.hiddenClients.includes(c)} onChange={() => onChange({ ...filters, hiddenClients: toggle(filters.hiddenClients, c) })} />
              {c}
            </label>
          ))}
        </div>
      )}
      {model.paths.length > 0 && (
        <div className="group">
          <span>paths</span>
          {model.paths.map((p) => (
            <label key={p} title={p}>
              <input type="checkbox" checked={!filters.hiddenPaths.includes(p)} onChange={() => onChange({ ...filters, hiddenPaths: toggle(filters.hiddenPaths, p) })} />
              {p.replace(/^\/peer\//, "")}
            </label>
          ))}
        </div>
      )}
    </div>
  );
}
