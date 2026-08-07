export interface SearchScope {
  label: string;
  prefixes: string[];
  others?: boolean;
}

export function matchesActiveScopes(
  id: string,
  scopes: SearchScope[],
  activeIndices: Set<number>,
): boolean {
  if (activeIndices.size === 0) return true;

  const hasOthers = [...activeIndices].some((i) => scopes[i]?.others);
  const prefixes = [...activeIndices].flatMap((i) =>
    scopes[i]?.others ? [] : scopes[i]?.prefixes || [],
  );

  if (prefixes.some((p) => id.startsWith(p))) return true;

  if (hasOthers) {
    const allPrefixes = scopes.flatMap((s) => (s.others ? [] : s.prefixes));
    return !allPrefixes.some((p) => id.startsWith(p));
  }

  return false;
}
