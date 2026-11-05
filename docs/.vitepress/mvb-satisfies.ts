import semver from "semver";

/**
 * Build an mvb `satisfies` range that keeps only the latest patch of each
 * minor among tags that already satisfy `range`.
 *
 * mvb tests each git tag with `semver.satisfies(tag, range)` independently, so
 * the range itself must reject older patches. Emit the selected versions joined
 * with `||` (a range-set of exact comparators).
 *
 * When `maxMinors` is given, keep only the newest `maxMinors` minors (by
 * semver, after the per-minor latest-patch selection). It must be an integer
 * >= 1; anything else throws a RangeError rather than silently dropping the cap.
 *
 * If no tag qualifies, return `range` unchanged.
 */
export function getLatestPatchMatching(tags: string[], range: string, maxMinors?: number): string {
  if (maxMinors !== undefined && (!Number.isInteger(maxMinors) || maxMinors < 1)) {
    throw new RangeError(`maxMinors must be an integer >= 1, got ${maxMinors}`);
  }
  const versions = tags
    .map((tag) => semver.clean(tag))
    .filter((version): version is string => version !== null)
    .filter((version) => semver.prerelease(version) === null)
    .filter((version) => semver.satisfies(version, range));

  const minors = [
    ...new Set(semver.sort(versions).map((v) => `${semver.major(v)}.${semver.minor(v)}`)),
  ];
  let latest = minors
    .map((minor) => semver.maxSatisfying(versions, `~${minor}`))
    .filter((version): version is string => version !== null);
  if (maxMinors !== undefined) {
    // `latest` is in ascending semver order; keep the newest entries.
    latest = latest.slice(Math.max(latest.length - maxMinors, 0));
  }
  return latest.length > 0 ? latest.join(" || ") : range;
}
