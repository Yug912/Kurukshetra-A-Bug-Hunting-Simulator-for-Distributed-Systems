import { opLabel, opOutcome } from "../data/derive";
import type { RunModel } from "../data/model";
import { ExplainPanel } from "./Explain";
import type { Selection } from "./Timeline";

/** describe returns a friendly sentence and the raw records for a selection. */
export function describe(sel: NonNullable<Selection>, model: RunModel): { title: string; text: string; raw: unknown } {
  switch (sel.kind) {
    case "message": {
      const m = sel.msg;
      let fate: string;
      if (m.drop !== null) fate = `dropped at step ${m.drop}${m.dropReason ? ` (${m.dropReason})` : ""}`;
      else if (m.deliveries.length === 0) fate = "never delivered (still in flight at the end)";
      else fate = `delivered at step ${m.deliveries.join(" and ")}`;
      const extra = [m.delaySteps > 0 ? `delayed ${m.delaySteps} steps` : "", m.dup ? "duplicated" : ""].filter(Boolean).join(", ");
      return {
        title: `Message ${m.id}`,
        text: `${m.from} → ${m.to} ${m.path}, ${m.size} bytes, sent at step ${m.send}, ${fate}${extra ? `; ${extra}` : ""}.`,
        raw: m.events,
      };
    }
    case "op": {
      const p = sel.op;
      const out = opOutcome(p);
      const checks = (model.run.result?.checks ?? []).filter((c) => (c.bad_ops ?? []).some((i) => i === p.invoke.index || i === p.complete?.index));
      const blame = checks.map((c) => `${c.checker}: ${c.reason ?? c.verdict}`).join("; ");
      return {
        title: `Op ${p.invoke.index}${p.complete ? `/${p.complete.index}` : ""}${p.invoke.final ? " (final read)" : ""}`,
        text:
          `Client ${p.client} ran ${opLabel(p)} on ${p.invoke.node} from step ${p.start} to ${p.end ?? "the end (never completed)"}: ${out}` +
          `${p.complete?.error ? ` (${p.complete.error})` : ""}.` +
          (p.bad ? ` Flagged by ${blame || "a checker"}.` : ""),
        raw: [p.invoke, p.complete],
      };
    }
    case "interval": {
      const iv = sel.iv;
      const what = sel.what === "partition" ? `Partition ${iv.label.replace(/\|/g, " | ")}` : `${sel.what === "crash" ? "Crash" : "Pause"} of ${iv.node}`;
      const fault = model.run.faults.find((f) => f.step === iv.start && (sel.what === "partition" ? f.kind === "partition" : f.kind === sel.what));
      return {
        title: what,
        text: `${what} from step ${iv.start} to ${iv.open ? "the end of the run" : iv.end} (${iv.end - iv.start} steps).`,
        raw: { interval: iv, fault: fault ?? null },
      };
    }
    case "skew":
      return {
        title: `Clock skew on ${sel.skew.node}`,
        text: `At step ${sel.skew.step} the logical clock of ${sel.skew.node} was set to ${sel.skew.offsetMs} ms.`,
        raw: sel.skew.event,
      };
  }
}

/** Details is the right-hand panel for the clicked item. */
export function Details({ selection, model, children }: { selection: Selection; model: RunModel; children?: React.ReactNode }) {
  return (
    <div className="details">
      {children}
      {model.run.explain ? <ExplainPanel text={model.run.explain} /> : null}
      {!selection ? (
        <p className="note" style={{ padding: 0 }}>
          Click a message, op, partition, crash or skew badge to see its details. Scroll to zoom, drag to pan.
        </p>
      ) : (
        <SelectionView selection={selection} model={model} />
      )}
    </div>
  );
}

function SelectionView({ selection, model }: { selection: NonNullable<Selection>; model: RunModel }) {
  const d = describe(selection, model);
  return (
    <>
      <h2>{d.title}</h2>
      <div className="friendly">{d.text}</div>
      <pre>{JSON.stringify(d.raw, null, 2)}</pre>
    </>
  );
}
