"""Tests for hack/check-gateway-doc-configs.

agentgateway is replaced by a stand-in that records each config it is asked
to validate and rejects one that contains the key `notAField`, the way the
real `--validate-only` rejects an unknown field.
"""

import json
import os
import shutil
import stat
import subprocess
import sys
from pathlib import Path

import pytest
import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "hack" / "check-gateway-doc-configs"
PAGE = REPO_ROOT / "docs" / "guides" / "infrastructure" / "inference-gateway-operator.md"
EXAMPLES = PAGE.parent / "examples" / "inference-gateway"
DIGEST = "sha256:9d3e6044ddcdc0878b1787f77bd401252b95e22684203fb5e874c4c42d2ed90c"

FAKE_AGENTGATEWAY = """\
import os, sys, time, yaml

def has_bad_key(node):
    if isinstance(node, dict):
        return "notAField" in node or any(has_bad_key(v) for v in node.values())
    if isinstance(node, list):
        return any(has_bad_key(v) for v in node)
    return False

args = sys.argv[1:]
if args == ["-V"]:
    print(os.environ.get("FAKE_AGW_VERSION", "1.6.0"))
    sys.exit(0)
config = args[args.index("-f") + 1]
text = open(config).read()
log = os.environ["FAKE_AGW_LOG"]
with open(os.path.join(log, "%d.yaml" % time.monotonic_ns()), "w") as f:
    f.write(text)
data = yaml.safe_load(text)
for model in data["llm"]["models"]:
    key = model.get("params", {}).get("apiKey", {})
    if isinstance(key, dict) and "file" in key and not os.path.isfile(key["file"]):
        print("Error: apiKey file %s: No such file or directory" % key["file"])
        sys.exit(1)
if has_bad_key(data):
    print("Error: unknown field `notAField`")
    sys.exit(1)
print("Configuration is valid!")
"""

# Records its arguments, then validates like agentgateway with the mounted
# paths mapped back to the host.
FAKE_DOCKER = """\
import json, os, subprocess, sys

args = sys.argv[1:]
with open(os.path.join(os.environ["FAKE_AGW_LOG"], "docker-calls.jsonl"), "a") as f:
    f.write(json.dumps(args) + "\\n")
if args[0] == "pull":
    sys.exit(0)
mounts, rest, i = {}, [], 1
while i < len(args):
    if args[i] == "-v":
        src, dst, _ = args[i + 1].split(":")
        mode = os.stat(src).st_mode & 0o777
        if mode != 0o755:
            print("mount %s is not world-readable: %o" % (src, mode))
            sys.exit(1)
        mounts[dst] = src
        i += 2
    elif args[i] == "--rm":
        i += 1
    else:
        rest = args[i + 1:]
        break
config = rest[rest.index("-f") + 1]
for dst, src in mounts.items():
    config = config.replace(dst, src)
text = open(config).read()
for dst, src in mounts.items():
    text = text.replace(dst, src)
host_config = config + ".host.yaml"
open(host_config, "w").write(text)
rest[rest.index("-f") + 1] = host_config
sys.exit(subprocess.run([sys.executable, os.environ["FAKE_AGW_SCRIPT"]] + rest).returncode)
"""


def write_executable(path: Path, body: str) -> Path:
    path.write_text(f"#!{sys.executable}\n{body}")
    path.chmod(path.stat().st_mode | stat.S_IXUSR)
    return path


@pytest.fixture
def env(tmp_path):
    log = tmp_path / "log"
    log.mkdir()
    fake = write_executable(tmp_path / "agentgateway", FAKE_AGENTGATEWAY)
    e = {k: v for k, v in os.environ.items() if k not in ("AGENTGATEWAY_BIN", "CONTAINER_RUNTIME")}
    e.update(
        {"FAKE_AGW_LOG": str(log), "FAKE_AGW_SCRIPT": str(fake), "AGENTGATEWAY_BIN": str(fake)}
    )
    return e


@pytest.fixture
def docs(tmp_path):
    """A copy of the page and its example files that a test can break."""
    page_dir = tmp_path / "docs"
    shutil.copytree(EXAMPLES, page_dir / "examples" / "inference-gateway")
    shutil.copy(PAGE, page_dir / PAGE.name)
    return page_dir / PAGE.name


def run(env, page=PAGE):
    examples = page.parent / "examples" / "inference-gateway"
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--page", str(page), "--examples-dir", str(examples)],
        capture_output=True,
        text=True,
        env=env,
    )


def validated_configs(env):
    log = Path(env["FAKE_AGW_LOG"])
    return [
        yaml.safe_load(p.read_text())
        for p in sorted(log.glob("*.yaml"))
        if not p.name.endswith(".host.yaml")
    ]


def replace_once(path: Path, old: str, new: str):
    text = path.read_text()
    assert text.count(old) == 1, f"expected exactly one {old!r} in {path}"
    path.write_text(text.replace(old, new))


def test_page_as_written_passes(env):
    result = run(env)
    assert result.returncode == 0, result.stderr
    assert result.stdout.count("OK: ") == 4

    configs = validated_configs(env)
    assert len(configs) == 4
    names = [[m["name"] for m in c["llm"]["models"]] for c in configs]
    assert ["claude-sonnet-4-6", "gemini-2.5-flash"] in names
    assert ["claude-sonnet-4-6", "gemini-2.5-flash", "gpt-6-luna"] in names
    assert ["claude-sonnet-4-6", "gemini-2.5-flash", "echo", "echo-noremove"] in names

    custody = next(c for c in configs if "binds" in c)
    body = custody["binds"][0]["listeners"][0]["routes"][0]["policies"]["directResponse"][
        "bodyExpression"
    ]
    assert "Bearer placeholder-echo-key" in body, "ECHO_KEY was not filled in"

    keyed = next(c for c in configs if "apiKey" in c["llm"]["policies"])
    assert keyed["llm"]["policies"]["jwtAuth"]["mode"] == "permissive"
    for model in keyed["llm"]["models"]:
        assert {"allow": 'apiKey.purpose == "example-repo-key"'} in model["authorization"]["rules"]

    for config in configs:
        audiences = config["llm"]["policies"]["jwtAuth"]["providers"][0]["audiences"]
        assert audiences == ["fullsend-inference"]


def test_broken_base_config_fails(env, docs):
    replace_once(docs, "  discovery: disabled\n", "  discovery: disabled\n  notAField: true\n")
    result = run(env, docs)
    assert result.returncode == 1
    assert "ERROR: step 3 config: agentgateway v1.6.0 rejected the config" in result.stderr
    assert "unknown field `notAField`" in result.stderr
    assert "4 of 4 configs" in result.stderr


def test_broken_fragment_fails_only_its_variant(env, docs):
    replace_once(docs, "    provider: openAI\n", "    provider: openAI\n    notAField: true\n")
    result = run(env, docs)
    assert result.returncode == 1
    assert "ERROR: step 3 + upstream that needs an API key" in result.stderr
    assert "1 of 4 configs" in result.stderr


def test_broken_example_file_fails(env, docs):
    echo = docs.parent / "examples" / "inference-gateway" / "echo-upstream.yaml"
    replace_once(echo, "- port: 9000\n", "- port: 9000\n  notAField: true\n")
    result = run(env, docs)
    assert result.returncode == 1
    assert "ERROR: step 3 + step 9 custody check" in result.stderr


def test_invalid_yaml_fails_before_validating(env, docs):
    replace_once(docs, "  discovery: disabled\n", "  discovery: [disabled\n")
    result = run(env, docs)
    assert result.returncode == 1
    assert "(base): not valid YAML" in result.stderr
    assert validated_configs(env) == []


def test_unmarked_yaml_block_fails(env, docs):
    with docs.open("a") as f:
        f.write("\n```yaml\nnew: block\n```\n")
    result = run(env, docs)
    assert result.returncode == 1
    assert "yaml block has no marker" in result.stderr


def test_missing_marker_fails(env, docs):
    replace_once(docs, "<!-- gateway-doc-check: custody-models -->\n", "")
    result = run(env, docs)
    assert result.returncode == 1
    assert "no yaml block is marked 'custody-models'" in result.stderr


def test_excerpt_that_drifted_from_its_file_fails(env, docs):
    workflow = docs.parent / "examples" / "inference-gateway" / "gateway-check.yml"
    replace_once(workflow, "timeout-minutes: 10", "timeout-minutes: 15")
    result = run(env, docs)
    assert result.returncode == 1
    assert "excerpt does not match gateway-check.yml" in result.stderr


def test_page_naming_two_versions_fails(env, docs):
    replace_once(docs, "agentgateway:v1.6.0 -f", "agentgateway:v1.7.0 -f")
    result = run(env, docs)
    assert result.returncode == 1
    assert "more than one agentgateway version: v1.6.0, v1.7.0" in result.stderr


def test_binary_of_another_version_fails(env):
    env["FAKE_AGW_VERSION"] = "1.5.0"
    result = run(env)
    assert result.returncode == 1
    assert "is version '1.5.0', but the page names v1.6.0" in result.stderr


def test_container_runtime_uses_pinned_image(env, tmp_path):
    bindir = tmp_path / "bin"
    bindir.mkdir()
    write_executable(bindir / "docker", FAKE_DOCKER)
    del env["AGENTGATEWAY_BIN"]
    env["PATH"] = f"{bindir}{os.pathsep}{env['PATH']}"
    result = run(env)
    assert result.returncode == 0, result.stdout + result.stderr

    calls = [
        json.loads(line)
        for line in (Path(env["FAKE_AGW_LOG"]) / "docker-calls.jsonl").read_text().splitlines()
    ]
    image = f"ghcr.io/agentgateway/agentgateway:v1.6.0@{DIGEST}"
    assert calls[0] == ["pull", "--quiet", image]
    assert len(calls) == 5
    for call in calls[1:]:
        assert call[call.index(image) + 1 :] == [
            "-f",
            "/etc/agw-config/config.yaml",
            "--validate-only",
        ]
        assert any(arg.endswith(":/etc/agw-upstream:ro") for arg in call)


def test_no_runtime_fails_with_instructions(env, tmp_path):
    del env["AGENTGATEWAY_BIN"]
    env["CONTAINER_RUNTIME"] = "no-such-runtime"
    result = run(env)
    assert result.returncode == 1
    assert "install docker or podman, or set AGENTGATEWAY_BIN" in result.stderr
