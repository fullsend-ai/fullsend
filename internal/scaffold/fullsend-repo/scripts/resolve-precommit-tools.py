#!/usr/bin/env python3
"""Resolve pre-commit hook tool dependencies for a target repository.

Reads a target repo's .pre-commit-config.yaml, matches hooks against
the known-tools registry (.pre-commit-tools.yaml), and outputs a JSON
manifest to stdout.

Usage:
    resolve-precommit-tools.py <target-repo-path> [--local-registry <path>]
"""

import argparse
import json
import os
import subprocess
import sys

try:
    import yaml
except ImportError:
    try:
        subprocess.check_call(
            [
                sys.executable,
                "-m",
                "pip",
                "install",
                "--quiet",
                "--no-deps",
                "--break-system-packages",
                "pyyaml==6.0.2",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        import yaml
    except Exception:
        print('{"tools":[],"warnings":["failed to install pyyaml — cannot resolve hooks"]}')
        sys.exit(0)


def merge_registries(upstream: dict, local: dict) -> tuple[dict, list[str]]:
    """Merge a per-repo local registry into the upstream/org registry.

    Returns (merged_registry, warnings). Local entries extend by default,
    override by matching (repo, hook_id) key, or suppress with exclude: true.
    """
    warnings: list[str] = []

    if not isinstance(local, dict) or "tools" not in local:
        warnings.append("per-repo registry is invalid (missing 'tools' key) — using upstream only")
        return upstream, warnings

    local_tools = local.get("tools")
    if not isinstance(local_tools, list):
        warnings.append("per-repo registry 'tools' is not a list — using upstream only")
        return upstream, warnings

    upstream_tools = (upstream.get("tools") or [])[:]
    keyed: dict[tuple[str, str], int] = {}
    match_entry_owners: dict[str, tuple[str, str]] = {}
    for i, tool in enumerate(upstream_tools):
        if isinstance(tool, dict) and "hook_id" in tool:
            key = (tool.get("repo", ""), tool["hook_id"])
            keyed[key] = i
            if "match_entry" in tool:
                match_entry_owners[tool["match_entry"]] = key

    for entry in local_tools:
        if not isinstance(entry, dict) or "hook_id" not in entry:
            warnings.append(f"skipping invalid per-repo entry (missing hook_id): {entry!r}")
            continue

        key = (entry.get("repo", ""), entry["hook_id"])

        if entry.get("exclude") is True:
            idx = keyed.get(key)
            if idx is not None:
                upstream_tools[idx] = None  # type: ignore[assignment]
                match_val = entry.get("match_entry")
                if match_val and match_entry_owners.get(match_val) == key:
                    del match_entry_owners[match_val]
                del keyed[key]
            continue

        if "match_entry" in entry:
            match_val = entry["match_entry"]
            existing_owner = match_entry_owners.get(match_val)
            if existing_owner is not None and existing_owner != key:
                warnings.append(
                    f"per-repo entry {key} has match_entry '{match_val}' "
                    f"that collides with upstream entry {existing_owner} "
                    f"— the upstream match will be shadowed"
                )
            match_entry_owners[match_val] = key

        idx = keyed.get(key)
        if idx is not None:
            upstream_tools[idx] = entry
        else:
            keyed[key] = len(upstream_tools)
            upstream_tools.append(entry)

    merged = [t for t in upstream_tools if t is not None]
    return {"tools": merged}, warnings


def resolve(precommit_path: str, registry: dict) -> dict:
    """Resolve tool dependencies from a .pre-commit-config.yaml against a registry dict."""
    try:
        with open(precommit_path) as f:
            precommit = yaml.safe_load(f)
    except (yaml.YAMLError, OSError) as exc:
        return {"tools": [], "warnings": [f"failed to parse .pre-commit-config.yaml: {exc}"]}

    if not isinstance(precommit, dict) or "repos" not in precommit:
        return {"tools": [], "warnings": ["empty or invalid .pre-commit-config.yaml"]}

    repos = precommit["repos"]
    if not isinstance(repos, list):
        return {"tools": [], "warnings": ["repos field is not a list in .pre-commit-config.yaml"]}

    if not isinstance(registry, dict) or "tools" not in registry:
        return {"tools": [], "warnings": ["empty or invalid tools registry"]}

    registry_tools = registry.get("tools") or []

    repo_hook_map = {}
    entry_match_map = {}
    for tool in registry_tools:
        if not isinstance(tool, dict) or "hook_id" not in tool:
            continue
        key = (tool.get("repo", ""), tool["hook_id"])
        repo_hook_map[key] = tool
        if "match_entry" in tool:
            entry_match_map[tool["match_entry"]] = tool

    resolved = []
    seen_names: set[str] = set()
    warnings = []

    for repo_entry in repos:
        if not isinstance(repo_entry, dict):
            continue
        repo_url = repo_entry.get("repo", "")
        for hook in repo_entry.get("hooks") or []:
            if not isinstance(hook, dict):
                continue
            hook_id = hook.get("id", "")
            entry = hook.get("entry", "")
            language = hook.get("language", "")

            tool = repo_hook_map.get((repo_url, hook_id))

            if tool is None and repo_url == "local":
                parts = entry.split()
                entry_cmd = parts[0] if parts else ""
                for match_str, match_tool in entry_match_map.items():
                    if entry_cmd == match_str:
                        tool = match_tool
                        break

            if tool is not None:
                install = tool.get("install") or {}
                name = install.get("name", "")
                if name and name not in seen_names:
                    seen_names.add(name)
                    resolved.append(install)
            else:
                if language == "system":
                    parts = entry.split()
                    cmd = parts[0] if parts else hook_id
                    warnings.append(
                        f"hook '{hook_id}' uses language:system "
                        f"(command: {cmd}) — not in registry, "
                        f"must be pre-installed on runner"
                    )
                elif language in ("golang",):
                    warnings.append(
                        f"hook '{hook_id}' requires Go toolchain (language: {language})"
                    )
                elif language in ("rust",):
                    warnings.append(
                        f"hook '{hook_id}' requires Rust toolchain (language: {language})"
                    )

    return {"tools": resolved, "warnings": warnings}


def load_yaml_file(path: str) -> tuple[dict | None, str | None]:
    """Load and parse a YAML file. Returns (data, error_message)."""
    try:
        with open(path) as f:
            data = yaml.safe_load(f)
        return data, None
    except (yaml.YAMLError, OSError) as exc:
        return None, str(exc)


def main():
    parser = argparse.ArgumentParser(description="Resolve pre-commit tool dependencies")
    parser.add_argument("target_repo", help="Path to the target repository")
    parser.add_argument(
        "--local-registry",
        help="Path to a per-repo .pre-commit-tools.yaml (extracted from base branch)",
    )
    args = parser.parse_args()

    precommit_config = os.path.join(args.target_repo, ".pre-commit-config.yaml")

    if not os.path.isfile(precommit_config):
        print('{"tools":[],"warnings":["no .pre-commit-config.yaml found"]}')
        sys.exit(0)

    script_dir = os.path.dirname(os.path.abspath(__file__))
    registry_path = os.path.join(script_dir, ".pre-commit-tools.yaml")

    if not os.path.isfile(registry_path):
        print('{"tools":[],"warnings":["tools registry not found"]}')
        sys.exit(0)

    registry, err = load_yaml_file(registry_path)
    if err or not isinstance(registry, dict):
        print(json.dumps({"tools": [], "warnings": [f"failed to parse tools registry: {err}"]}))
        sys.exit(0)

    all_warnings: list[str] = []

    if args.local_registry and os.path.isfile(args.local_registry):
        local, err = load_yaml_file(args.local_registry)
        if err:
            all_warnings.append(f"failed to parse per-repo registry: {err}")
        elif local is None:
            all_warnings.append("per-repo registry is empty — using upstream only")
        elif isinstance(local, dict):
            registry, merge_warnings = merge_registries(registry, local)
            all_warnings.extend(merge_warnings)

    result = resolve(precommit_config, registry)
    if all_warnings:
        result.setdefault("warnings", []).extend(all_warnings)
    print(json.dumps(result))


if __name__ == "__main__":
    main()
