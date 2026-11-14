/** Parses JSON Lines, skipping blank lines. Throws with the line number on bad JSON. */
export function parseJSONL<T>(text: string): T[] {
  const out: T[] = [];
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line) continue;
    try {
      out.push(JSON.parse(line) as T);
    } catch (e) {
      throw new Error(`line ${i + 1}: ${(e as Error).message}`);
    }
  }
  return out;
}
