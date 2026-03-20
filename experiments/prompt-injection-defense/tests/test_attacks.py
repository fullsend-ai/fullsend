# tests/test_attacks.py
import pytest
from defenses.attacks import load_all_attacks, load_attack


def test_load_attack_from_file(tmp_path):
    attack_file = tmp_path / "test-attack.yaml"
    attack_file.write_text(
        'name: "test-attack"\n'
        'description: "A test attack"\n'
        'target_defense: "none"\n'
        'commit_message: "fix: update readme"\n'
        'injection_goal: "none"\n'
    )
    attack = load_attack(attack_file)
    assert attack.name == "test-attack"
    assert attack.commit_message == "fix: update readme"
    assert attack.target_defense == "none"


def test_load_attack_missing_field(tmp_path):
    attack_file = tmp_path / "bad.yaml"
    attack_file.write_text('name: "bad"\n')
    with pytest.raises(ValueError, match="missing required field"):
        load_attack(attack_file)


def test_load_all_attacks(tmp_path):
    for i in range(3):
        f = tmp_path / f"attack-{i}.yaml"
        f.write_text(
            f'name: "attack-{i}"\n'
            f'description: "Attack {i}"\n'
            f'target_defense: "none"\n'
            f'commit_message: "payload {i}"\n'
            f'injection_goal: "none"\n'
        )
    (tmp_path / "readme.md").write_text("not yaml")
    attacks = load_all_attacks(tmp_path)
    assert len(attacks) == 3
