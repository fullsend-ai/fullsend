# tests/test_combined.py
from unittest.mock import patch

from defenses.combined import run_combined
from defenses.interface import DefenseResult


@patch("defenses.combined.run_spotlighting")
@patch("defenses.combined.run_sandwiching")
@patch("defenses.combined.run_classifier")
def test_all_clean_returns_not_detected(mock_cls, mock_sand, mock_spot):
    mock_spot.return_value = DefenseResult(detected=False, explanation="clean")
    mock_sand.return_value = DefenseResult(detected=False, explanation="clean")
    mock_cls.return_value = DefenseResult(detected=False, explanation="clean")

    result = run_combined("fix: readme", injection_goal="none")
    assert result.detected is False


@patch("defenses.combined.run_spotlighting")
@patch("defenses.combined.run_sandwiching")
@patch("defenses.combined.run_classifier")
def test_one_detected_returns_detected(mock_cls, mock_sand, mock_spot):
    mock_spot.return_value = DefenseResult(detected=False, explanation="clean")
    mock_sand.return_value = DefenseResult(detected=True, explanation="caught it")
    mock_cls.return_value = DefenseResult(detected=False, explanation="clean")

    result = run_combined("payload", injection_goal="none")
    assert result.detected is True
    assert "sandwiching" in result.explanation.lower()


@patch("defenses.combined.run_spotlighting")
@patch("defenses.combined.run_sandwiching")
@patch("defenses.combined.run_classifier")
def test_all_detected_returns_detected(mock_cls, mock_sand, mock_spot):
    mock_spot.return_value = DefenseResult(detected=True, explanation="spot")
    mock_sand.return_value = DefenseResult(detected=True, explanation="sand")
    mock_cls.return_value = DefenseResult(detected=True, explanation="cls")

    result = run_combined("payload", injection_goal="none")
    assert result.detected is True
