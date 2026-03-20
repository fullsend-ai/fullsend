# runner.py
import json
from pathlib import Path

from defenses.attacks import load_all_attacks
from defenses.classifier import run_classifier
from defenses.combined import run_combined
from defenses.interface import Attack, DefenseResult
from defenses.no_defense import run_no_defense
from defenses.sandwiching import run_sandwiching
from defenses.spotlighting import run_spotlighting

DEFENSES = {
    "no_defense": run_no_defense,
    "spotlighting": run_spotlighting,
    "sandwiching": run_sandwiching,
    "classifier": run_classifier,
    "combined": run_combined,
}

RUNS_PER_CELL = 10


def summarize_cell(results: list[DefenseResult]) -> str:
    detected = sum(1 for r in results if r.detected)
    clean = len(results) - detected
    if detected == len(results):
        return f"detected ({detected}/{len(results)})"
    if clean == len(results):
        return f"clean ({clean}/{len(results)})"
    return f"**{clean}/{len(results)} clean**"


def run_matrix(attacks: list[Attack]) -> dict[tuple[str, str], list[DefenseResult]]:
    results: dict[tuple[str, str], list[DefenseResult]] = {}
    for attack in attacks:
        for defense_name, defense_fn in DEFENSES.items():
            key = (attack.name, defense_name)
            cell_results = []
            for run in range(RUNS_PER_CELL):
                print(f"  [{run + 1}/{RUNS_PER_CELL}] {attack.name} x {defense_name}...")
                result = defense_fn(
                    attack.commit_message, attack.injection_goal, attack.expected_assessment
                )
                cell_results.append(result)
            results[key] = cell_results
    return results


def format_results_table(results: dict[tuple[str, str], list[DefenseResult]]) -> str:
    attack_names = sorted(set(k[0] for k in results))
    defense_names = list(DEFENSES.keys())

    header = "| Attack | " + " | ".join(defense_names) + " |"
    separator = "|" + "|".join(["---"] * (len(defense_names) + 1)) + "|"
    rows = []
    for attack_name in attack_names:
        cells = []
        for defense_name in defense_names:
            key = (attack_name, defense_name)
            if key in results:
                cells.append(summarize_cell(results[key]))
            else:
                cells.append("-")
        rows.append(f"| {attack_name} | " + " | ".join(cells) + " |")

    return "\n".join([header, separator] + rows)


def save_results(results: dict[tuple[str, str], list[DefenseResult]], output_dir: Path):
    table = format_results_table(results)

    results_md = output_dir / "results.md"
    results_md.write_text(f"# Results\n\n{table}\n")
    print(f"\nResults table written to {results_md}")

    raw = {}
    for (attack_name, defense_name), cell_results in results.items():
        key = f"{attack_name}__{defense_name}"
        raw[key] = [
            {
                "detected": r.detected,
                "explanation": r.explanation,
                "raw_output": r.raw_output,
            }
            for r in cell_results
        ]
    raw_path = output_dir / "results-raw.json"
    raw_path.write_text(json.dumps(raw, indent=2))
    print(f"Raw results written to {raw_path}")


def main():
    project_dir = Path(__file__).parent
    attacks = load_all_attacks(project_dir / "attacks")
    print(f"Loaded {len(attacks)} attacks")
    print(f"Running {len(attacks)} attacks x {len(DEFENSES)} defenses x {RUNS_PER_CELL} runs")
    print()

    results = run_matrix(attacks)

    print("\n" + format_results_table(results))
    save_results(results, project_dir)


if __name__ == "__main__":
    main()
