import semver from "semver";

/**
 * Build an mvb `satisfies` range that keeps only the latest patch of each
 * minor among tags that already satisfy `range`.
 *
 * mvb tests each git tag with `semver.satisfies(tag, range)` independently, so
 * the range itself must reject older patches. Emit the selected versions joined
 * with `||` (a range-set of exact comparators).
 *
 * If no tag qualifies, return `range` unchanged.
 */
export function getLatestPatchMatching(tags: string[], range: string): string {
  const versions = tags
    .map((tag) => semver.clean(tag))
    .filter((version): version is string => version !== null)
    .filter((version) => semver.prerelease(version) === null)
    .filter((version) => semver.satisfies(version, range));

  const minors = [
    ...new Set(semver.sort(versions).map((v) => `${semver.major(v)}.${semver.minor(v)}`)),
  ];
  const latest = minors
    .map((minor) => semver.maxSatisfying(versions, `~${minor}`))
    .filter((version): version is string => version !== null);
  return latest.length > 0 ? latest.join(" || ") : range;
}
