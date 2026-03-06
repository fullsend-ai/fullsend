import re
from dataclasses import dataclass
from pathlib import Path

import yaml


@dataclass
class StepImage:
    name: str
    full_ref: str
    image_repo: str


@dataclass
class TektonTask:
    name: str
    file_path: Path
    steps: list[StepImage]


def _extract_repo(image_ref: str) -> str:
    """Extract the repository part of an image reference, stripping tag and digest."""
    # Remove digest
    repo = image_ref.split("@")[0]
    # Remove tag
    repo = re.split(r":[^/]", repo)[0]
    return repo


def parse_task(path: Path) -> TektonTask | None:
    with open(path) as f:
        doc = yaml.safe_load(f)

    if not doc or doc.get("kind") != "Task":
        return None

    name = doc.get("metadata", {}).get("name", path.stem)
    steps = []
    for step in doc.get("spec", {}).get("steps", []):
        image = step.get("image", "")
        steps.append(
            StepImage(
                name=step.get("name", ""),
                full_ref=image,
                image_repo=_extract_repo(image),
            )
        )

    return TektonTask(name=name, file_path=path, steps=steps)
