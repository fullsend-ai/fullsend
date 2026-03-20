# defenses/combined.py
from defenses.classifier import run_classifier
from defenses.interface import DefenseResult
from defenses.sandwiching import run_sandwiching
from defenses.spotlighting import run_spotlighting


def run_combined(
    commit_message: str, injection_goal: str, expected_assessment: str = "suspicious"
) -> DefenseResult:
    results = {
        "spotlighting": run_spotlighting(commit_message, injection_goal, expected_assessment),
        "sandwiching": run_sandwiching(commit_message, injection_goal, expected_assessment),
        "classifier": run_classifier(commit_message, injection_goal),
    }

    detected_by = [name for name, r in results.items() if r.detected]

    if detected_by:
        return DefenseResult(
            detected=True,
            explanation=f"Detected by: {', '.join(detected_by)}",
            raw_output=str({name: r.raw_output for name, r in results.items()}),
        )

    return DefenseResult(
        detected=False,
        explanation="All three defenses passed",
        raw_output=str({name: r.raw_output for name, r in results.items()}),
    )
