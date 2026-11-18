import { useCallback, useState } from "react";
import type { RunModel } from "../data/model";
import { clamp, type Viewport } from "../data/viewport";
import { Details } from "./Details";
import { EMPTY_FILTERS, type Filters } from "./filters";
import { Header } from "./Header";
import { Minimap } from "./Minimap";
import { Timeline, type Selection } from "./Timeline";
import { Toolbar } from "./Toolbar";
import { useWidth } from "./useWidth";

interface Props {
  model: RunModel;
  // Controlled state (optional): when given, the parent owns it (URL sync, compare mode).
  vp?: Viewport;
  onViewport?: (vp: Viewport) => void;
  filters?: Filters;
  onFilters?: (f: Filters) => void;
  playhead?: number;
  headerExtra?: React.ReactNode;
  above?: React.ReactNode; // e.g. the player bar
  side?: React.ReactNode; // extra content at the top of the details panel
  compact?: boolean; // hide toolbar and details (compare mode)
}

/** RunView is the timeline page for one run: header, filters, timeline, minimap, details. */
export function RunView(props: Props) {
  const { model } = props;
  const [ownVp, setOwnVp] = useState<Viewport>(() => clamp({ start: 0, end: model.totalSteps }, model.totalSteps));
  const [ownFilters, setOwnFilters] = useState<Filters>(EMPTY_FILTERS);
  const [selection, setSelection] = useState<Selection>(null);
  const [ref, width] = useWidth<HTMLDivElement>();
  const vp = props.vp ?? ownVp;
  const filters = props.filters ?? ownFilters;
  const setVp = useCallback((v: Viewport) => (props.onViewport ?? setOwnVp)(v), [props.onViewport]);
  const setFilters = props.onFilters ?? setOwnFilters;

  return (
    <>
      <Header model={model} extra={props.headerExtra} />
      {!props.compact && <Toolbar model={model} filters={filters} onChange={setFilters} />}
      {props.above}
      <div className="main">
        <div className="canvas">
          <div className="timeline" ref={ref}>
            <Timeline model={model} vp={vp} width={width} filters={filters} selection={selection} playhead={props.playhead} onSelect={setSelection} onViewport={setVp} />
          </div>
          <Minimap model={model} vp={vp} width={width} playhead={props.playhead} onViewport={setVp} />
        </div>
        {!props.compact && (
          <Details selection={selection} model={model}>
            {props.side}
          </Details>
        )}
      </div>
    </>
  );
}
