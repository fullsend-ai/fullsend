# tests/test_runner.py

from defenses.interface import DefenseResult
from runner import format_results_table, summarize_cell


def test_format_results_table():
    results = {
        ("benign", "no_defense"): [
            DefenseResult(detected=False, explanation="clean"),
            DefenseResult(detected=False, explanation="clean"),
            DefenseResult(detected=False, explanation="clean"),
        ],
        ("benign", "spotlighting"): [
            DefenseResult(detected=False, explanation="clean"),
            DefenseResult(detected=False, explanation="clean"),
            DefenseResult(detected=False, explanation="clean"),
        ],
    }
    table = format_results_table(results)
    lines = table.strip().split("\n")
    # Header + separator + 1 data row
    assert len(lines) == 3
    # Header has all defense columns
    assert "no_defense" in lines[0]
    assert "spotlighting" in lines[0]
    # Data row has attack name and results
    assert "benign" in lines[2]
    assert "clean" in lines[2]


def test_summarize_cell_all_clean():
    results = [
        DefenseResult(detected=False, explanation=""),
        DefenseResult(detected=False, explanation=""),
        DefenseResult(detected=False, explanation=""),
    ]
    assert summarize_cell(results) == "clean (3/3)"


def test_summarize_cell_all_detected():
    results = [
        DefenseResult(detected=True, explanation=""),
        DefenseResult(detected=True, explanation=""),
        DefenseResult(detected=True, explanation=""),
    ]
    assert summarize_cell(results) == "detected (3/3)"


def test_summarize_cell_mixed():
    results = [
        DefenseResult(detected=False, explanation=""),
        DefenseResult(detected=True, explanation=""),
        DefenseResult(detected=False, explanation=""),
    ]
    assert summarize_cell(results) == "**2/3 clean**"
