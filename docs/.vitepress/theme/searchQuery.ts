export interface ParsedQuery {
  /** The query string with double-quoted phrases stripped of their quotes. */
  query: string;
  /** Exact phrases extracted from double-quoted substrings. */
  phrases: string[];
}

/**
 * Parses a search query string, extracting double-quoted phrases.
 *
 * Returns the cleaned query (quotes removed, words kept for AND matching)
 * and an array of exact phrases that must appear adjacent in results.
 *
 * Examples:
 *   parseSearchQuery('eval scenario')
 *     => { query: 'eval scenario', phrases: [] }
 *   parseSearchQuery('"eval scenario"')
 *     => { query: 'eval scenario', phrases: ['eval scenario'] }
 *   parseSearchQuery('harness "eval scenario" config')
 *     => { query: 'harness eval scenario config', phrases: ['eval scenario'] }
 */
export function parseSearchQuery(raw: string): ParsedQuery {
  const phrases: string[] = [];
  const query = raw.replace(/"([^"]+)"/g, (_match, phrase: string) => {
    const trimmed = phrase.trim();
    if (trimmed) phrases.push(trimmed);
    // Pad with spaces so adjacent quoted phrases don't fuse tokens
    // (e.g. "foo bar""baz qux" → "foo bar baz qux", not "foo barbaz qux").
    return " " + trimmed + " ";
  });
  return { query: query.replace(/\s+/g, " ").trim(), phrases };
}

/**
 * Returns true when every phrase appears as a case-insensitive
 * substring in the given text.
 */
export function textContainsPhrases(text: string, phrases: string[]): boolean {
  if (phrases.length === 0) return true;
  const lower = text.toLowerCase();
  return phrases.every((p) => lower.includes(p.toLowerCase()));
}

/**
 * Filters search results by requiring every phrase to appear in the
 * result's concatenated title + text content.  Results with no text
 * content are kept (graceful degradation).
 */
export function filterByPhrases<
  T extends { text?: string; title?: string; titles?: string[] },
>(results: T[], phrases: string[]): T[] {
  if (phrases.length === 0) return results;
  return results.filter((r) => {
    const content = [...(r.titles || []), r.title || "", r.text || ""].join(" ");
    if (!content.trim()) return true;
    return textContainsPhrases(content, phrases);
  });
}
