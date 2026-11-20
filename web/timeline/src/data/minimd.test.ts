import { describe, expect, it } from "vitest";
import { parseInline, parseMiniMarkdown } from "./minimd";

describe("minimd", () => {
  it("parses bold and code runs", () => {
    expect(parseInline("**linearizable-kv** read `k0` on n1")).toEqual([
      { kind: "bold", text: "linearizable-kv" },
      { kind: "text", text: " read " },
      { kind: "code", text: "k0" },
      { kind: "text", text: " on n1" },
    ]);
  });

  it("groups headings, lists and paragraphs", () => {
    const md = "# Why x failed\n\n## Faults\n\n- Step 1: a\n- Step 2: b\n\n## Root cause\n\nline one\nline two\n\n_Rewritten by claude._\n";
    const b = parseMiniMarkdown(md);
    expect(b.map((x) => x.kind)).toEqual(["h1", "h2", "li", "h2", "p", "p"]);
    const li = b[2];
    expect(li.kind === "li" && li.items.length).toBe(2);
    const p = b[4];
    expect(p.kind === "p" && p.parts[0].text).toBe("line one line two");
    const note = b[5];
    expect(note.kind === "p" && note.parts[0].text).toBe("Rewritten by claude.");
  });
});
