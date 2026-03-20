import argparse
import json
from dataclasses import asdict
from pathlib import Path

from scanner.config import load_config
from scanner.scan import scan_directory


def main():
    parser = argparse.ArgumentParser(
        description="Scan Tekton tasks for ADR-0046 drift (non-task-runner images)",
    )
    parser.add_argument("repo_path", help="Path to the build-definitions repo (or similar)")
    parser.add_argument("--config", required=True, help="Path to scanner config YAML")
    parser.add_argument("--json", dest="json_output", action="store_true", help="Output as JSON")
    args = parser.parse_args()

    config = load_config(args.config)
    violations = scan_directory(Path(args.repo_path), config)

    if args.json_output:
        print(json.dumps([asdict(v) for v in violations], indent=2))
    else:
        if not violations:
            print("No ADR-0046 drift detected.")
        else:
            print(f"Found {len(violations)} step(s) not using the task runner image:\n")
            for v in violations:
                print(f"  Task: {v.task_name}")
                print(f"  File: {v.task_file}")
                print(f"  Step: {v.step_name}")
                print(f"  Image: {v.current_image}")
                print()

    raise SystemExit(1 if violations else 0)


if __name__ == "__main__":
    main()
