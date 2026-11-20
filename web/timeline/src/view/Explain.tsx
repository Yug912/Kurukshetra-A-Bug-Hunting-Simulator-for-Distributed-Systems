import type { ReactNode } from "react";
import { parseMiniMarkdown, type Inline } from "../data/minimd";

function inline(parts: Inline[]): ReactNode[] {
  return parts.map((p, i) => (p.kind === "bold" ? <strong key={i}>{p.text}</strong> : p.kind === "code" ? <code key={i}>{p.text}</code> : <span key={i}>{p.text}</span>));
}

/** ExplainPanel shows explain.md (from `kuru explain`) as a collapsible "Why it failed" section. */
export function ExplainPanel({ text }: { text: string }) {
  const blocks = parseMiniMarkdown(text);
  return (
    <details className="explain" open>
      <summary>Why it failed</summary>
      {blocks.map((b, i) => {
        if (b.kind === "h1") return null; // the title repeats the header
        if (b.kind === "h2") return <h3 key={i}>{inline(b.parts)}</h3>;
        if (b.kind === "li")
          return (
            <ul key={i}>
              {b.items.map((it, j) => (
                <li key={j}>{inline(it)}</li>
              ))}
            </ul>
          );
        return <p key={i}>{inline(b.parts)}</p>;
      })}
    </details>
  );
}
