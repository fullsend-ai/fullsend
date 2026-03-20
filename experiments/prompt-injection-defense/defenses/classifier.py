# defenses/classifier.py
from functools import lru_cache

from defenses.interface import DefenseResult

MODEL_NAME = "protectai/deberta-v3-base-prompt-injection"


@lru_cache(maxsize=1)
def _get_pipeline():
    from transformers import pipeline

    return pipeline("text-classification", model=MODEL_NAME)


def run_classifier(
    commit_message: str, injection_goal: str, expected_assessment: str = "suspicious"
) -> DefenseResult:
    pipe = _get_pipeline()
    result = pipe(commit_message)
    label = result[0]["label"]
    score = result[0]["score"]
    detected = label == "INJECTION"
    return DefenseResult(
        detected=detected,
        explanation=f"Classifier: {label} (confidence: {score:.3f})",
        raw_output=str(result),
    )
