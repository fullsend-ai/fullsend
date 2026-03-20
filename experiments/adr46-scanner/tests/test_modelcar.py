from pathlib import Path

from scanner.config import ScannerConfig
from scanner.detector import detect_drift
from scanner.parser import parse_task

FIXTURES = Path(__file__).parent / "fixtures"


def test_modelcar_oci_ta_drift():
    """The modelcar-oci-ta task should have violations per ADR-0046.

    Expected violations (steps NOT using task runner and NOT exempt):
    - download-model-files (oras)
    - create-modelcar-base-image (release-service-utils)
    - copy-model-files (ubi9/python-311)
    - push-image (oras)
    - sbom-generate (mobster)
    - upload-sbom (appstudio-utils)
    - report-sbom-url (yq)

    The only exempt step is use-trusted-artifact (build-trusted-artifacts).
    """
    config = ScannerConfig(
        task_runner_image="quay.io/konflux-ci/task-runner",
        exempt_images=["quay.io/konflux-ci/build-trusted-artifacts"],
    )
    task = parse_task(FIXTURES / "modelcar-oci-ta.yaml")
    assert task is not None
    violations = detect_drift(task, config)

    violating_steps = {v.step_name for v in violations}
    assert "use-trusted-artifact" not in violating_steps, "TA step should be exempt"
    assert "download-model-files" in violating_steps
    assert "create-modelcar-base-image" in violating_steps
    assert "copy-model-files" in violating_steps
    assert "push-image" in violating_steps
    assert "sbom-generate" in violating_steps
    assert "upload-sbom" in violating_steps
    assert "report-sbom-url" in violating_steps
    assert len(violations) == 7
