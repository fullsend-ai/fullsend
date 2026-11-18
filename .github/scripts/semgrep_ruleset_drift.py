#!/usr/bin/env python3
"""Compare only the allowlisted Semgrep rule sources in two archives."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import tarfile
from pathlib import Path, PurePosixPath


def read_allowlist(path: Path) -> list[str]:
    files: list[str] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        entry = line.strip()
        if not entry or entry.startswith("#"):
            continue
        relative = PurePosixPath(entry)
        if relative.is_absolute() or ".." in relative.parts or "security" not in relative.parts:
            raise ValueError(f"invalid ruleset source path: {entry}")
        files.append(relative.as_posix())
    if not files:
        raise ValueError("ruleset source allowlist is empty")
    if len(files) != len(set(files)):
        raise ValueError("ruleset source allowlist contains duplicate paths")
    return files


def read_selected_files(archive_path: Path, allowlist: set[str]) -> dict[str, bytes]:
    selected: dict[str, bytes] = {}
    with tarfile.open(archive_path, "r:gz") as archive:
        for member in archive:
            member_path = PurePosixPath(member.name)
            if member_path.is_absolute() or ".." in member_path.parts:
                continue
            if len(member_path.parts) < 2 or not member.isfile():
                continue
            relative = PurePosixPath(*member_path.parts[1:]).as_posix()
            if relative not in allowlist:
                continue
            if relative in selected:
                raise ValueError(f"duplicate ruleset source in archive: {relative}")
            source = archive.extractfile(member)
            if source is None:
                raise ValueError(f"cannot read ruleset source from archive: {relative}")
            selected[relative] = source.read()
    return selected


def compare_archives(
    pinned_archive: Path, upstream_archive: Path, allowlist: list[str]
) -> dict[str, object]:
    paths = set(allowlist)
    pinned = read_selected_files(pinned_archive, paths)
    upstream = read_selected_files(upstream_archive, paths)
    return {
        "changed_files": sorted(
            path
            for path in paths & pinned.keys() & upstream.keys()
            if pinned[path] != upstream[path]
        ),
        "missing_from_pinned": sorted(paths - pinned.keys()),
        "missing_from_upstream": sorted(paths - upstream.keys()),
    }


def compare_config_snapshot(pin_path: Path, upstream_config: Path) -> dict[str, str | bool]:
    expected = pin_path.read_text(encoding="utf-8").strip()
    if not re.fullmatch(r"[0-9a-f]{64}", expected):
        raise ValueError("p/security-audit config pin must be a full lowercase SHA-256")
    actual = hashlib.sha256(upstream_config.read_bytes()).hexdigest()
    return {
        "config_drift": expected != actual,
        "config_sha256_pinned": expected,
        "config_sha256_upstream": actual,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("pinned_archive", type=Path)
    parser.add_argument("upstream_archive", type=Path)
    parser.add_argument("allowlist", type=Path)
    parser.add_argument("--config-sha256-pin", type=Path)
    parser.add_argument("--upstream-config", type=Path)
    args = parser.parse_args()

    if (args.config_sha256_pin is None) != (args.upstream_config is None):
        parser.error("--config-sha256-pin and --upstream-config must be provided together")

    result: dict[str, object] = compare_archives(
        args.pinned_archive,
        args.upstream_archive,
        read_allowlist(args.allowlist),
    )
    if args.config_sha256_pin is not None and args.upstream_config is not None:
        result.update(compare_config_snapshot(args.config_sha256_pin, args.upstream_config))
    print(json.dumps(result, separators=(",", ":")))


if __name__ == "__main__":
    main()
