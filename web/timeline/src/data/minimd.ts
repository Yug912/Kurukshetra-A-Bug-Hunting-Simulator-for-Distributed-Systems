// A tiny Markdown subset for explain.md: # / ## headings, "- " lists, paragraphs,
// **bold**, `code` and _italic_ (shown as plain text). Rendered as React nodes, never as HTML.

export type Inline = { kind: "text" | "bold" | "code"; text: string };
export type Block = { kind: "h1" | "h2" | "p"; parts: Inline[] } | { kind: "li"; items: Inline[][] };

/** parseInline splits a line into text, **bold** and `code` runs. */
export function parseInline(s: string): Inline[] {
  const out: Inline[] = [];
  const re = /\*\*(.+?)\*\*|`([^`]+)`/g;
  let last = 0;
  for (let m = re.exec(s); m; m = re.exec(s)) {
    if (m.index > last) out.push({ kind: "text", text: s.slice(last, m.index) });
    out.push(m[1] !== undefined ? { kind: "bold", text: m[1] } : { kind: "code", text: m[2] });
    last = m.index + m[0].length;
  }
  if (last < s.length) out.push({ kind: "text", text: s.slice(last) });
  return out.map((p) => (p.kind === "text" ? { ...p, text: p.text.replace(/(^|\s)_([^_]+)_(?=\s|[.,]|$)/g, "$1$2") } : p));
}

/** parseMiniMarkdown turns explain.md into blocks. */
export function parseMiniMarkdown(md: string): Block[] {
  const blocks: Block[] = [];
  let para: string[] = [];
  const flush = () => {
    if (para.length) blocks.push({ kind: "p", parts: parseInline(para.join(" ")) });
    para = [];
  };
  for (const raw of md.split("\n")) {
    const line = raw.trimEnd();
    if (line.startsWith("## ")) {
      flush();
      blocks.push({ kind: "h2", parts: parseInline(line.slice(3)) });
    } else if (line.startsWith("# ")) {
      flush();
      blocks.push({ kind: "h1", parts: parseInline(line.slice(2)) });
    } else if (line.startsWith("- ")) {
      flush();
      const prev = blocks[blocks.length - 1];
      const item = parseInline(line.slice(2));
      if (prev && prev.kind === "li") prev.items.push(item);
      else blocks.push({ kind: "li", items: [item] });
    } else if (line.trim() === "") {
      flush();
    } else {
      para.push(line.trim());
    }
  }
  flush();
  return blocks;
}
