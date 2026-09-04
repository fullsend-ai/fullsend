#!/usr/bin/env python3
"""Unit tests for secret_redact_posttool.py hook."""

import json
import os
import subprocess
import sys
import unittest
from pathlib import Path

HOOK = str(Path(__file__).parent / "secret_redact_posttool.py")


def run_hook(tool_result: str) -> tuple[int, str, str]:
    """Run the hook script and return (exit_code, stdout, stderr)."""
    stdin_raw = json.dumps({"tool_name": "Bash", "tool_result": tool_result})
    proc = subprocess.run(
        [sys.executable, HOOK],
        input=stdin_raw,
        capture_output=True,
        text=True,
        timeout=10,
    )
    return proc.returncode, proc.stdout, proc.stderr


class TestEnvSecretRedaction(unittest.TestCase):
    """Tests for env_secret pattern (case-insensitive, underscore-delimited)."""

    def test_lowercase_env_var(self):
        _, stdout, _ = run_hook("export my_token=s3cr3t_value_here")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("s3cr3t_value_here", result["tool_result"])

    def test_mixed_case_env_var(self):
        _, stdout, _ = run_hook("My_Secret_Key=FAKE0000test_value")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("FAKE0000test_value", result["tool_result"])

    def test_uppercase_env_var(self):
        _, stdout, _ = run_hook("export API_KEY=superSecretValue123")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("superSecretValue123", result["tool_result"])

    def test_monkey_not_matched(self):
        """'monkey' contains 'KEY' but should not trigger env_secret."""
        _, stdout, _ = run_hook("monkey=abcdefghijklmnop")
        # stdout should be empty (no redaction) or not contain env_secret
        if stdout:
            result = json.loads(stdout)
            patterns = result.get("metadata", {}).get("patterns", [])
            self.assertNotIn("env_secret", patterns)

    def test_keyboard_not_matched(self):
        """'keyboard_layout' contains 'KEY' but should not trigger env_secret."""
        _, stdout, _ = run_hook("keyboard_layout=us-international-layout")
        if stdout:
            result = json.loads(stdout)
            patterns = result.get("metadata", {}).get("patterns", [])
            self.assertNotIn("env_secret", patterns)

    def test_authority_not_matched(self):
        """'authority_url' contains 'AUTH' but should not trigger env_secret."""
        _, stdout, _ = run_hook("authority_url=https://login.example.com")
        if stdout:
            result = json.loads(stdout)
            patterns = result.get("metadata", {}).get("patterns", [])
            self.assertNotIn("env_secret", patterns)


class TestJsonSecretRedaction(unittest.TestCase):
    """Tests for json_secret pattern (single/double quotes, substring keys)."""

    def test_double_quoted_json(self):
        _, stdout, _ = run_hook('{"password": "my-super-secret-pass-1234"}')
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("my-super-secret-pass-1234", result["tool_result"])

    def test_single_quoted_json(self):
        _, stdout, _ = run_hook("{'api_key': 'not-a-prefix-match-v4lue9'}")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("not-a-prefix-match-v4lue9", result["tool_result"])

    def test_capture_group_selection(self):
        """The last non-None capture group should be used as the secret."""
        _, stdout, _ = run_hook("{'token': 'not-a-prefix-match-v4lue9'}")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("not-a-prefix-match-v4lue9", result["tool_result"])


class TestDbPasswordRedaction(unittest.TestCase):
    """Tests for db_password pattern (embedded @, min length, greedy capture)."""

    def test_simple_db_password(self):
        _, stdout, _ = run_hook("postgres://admin:hunter2secret@db:5432/app")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("hunter2secret", result["tool_result"])

    def test_embedded_at_sign(self):
        _, stdout, _ = run_hook("postgres://user:P@ssw0rd1@host:5432/db")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("P@ssw0rd1", result["tool_result"])

    def test_multiple_at_signs(self):
        """Greedy quantifier should capture the full password up to the last @."""
        _, stdout, _ = run_hook("postgres://user:P@ss@w0rd@host:5432/db")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("P@ss@w0rd", result["tool_result"])

    def test_postgresql_scheme(self):
        """postgresql:// (with ql suffix) should also be matched."""
        _, stdout, _ = run_hook("postgresql://admin:hunter2secret@db:5432/app")
        self.assertTrue(stdout, "Expected redaction output")
        result = json.loads(stdout)
        self.assertNotIn("hunter2secret", result["tool_result"])

    def test_short_password_not_matched(self):
        """Passwords below minimum length (4) should not match."""
        _, stdout, _ = run_hook("postgres://user:abc@host:5432/db")
        if stdout:
            result = json.loads(stdout)
            patterns = result.get("metadata", {}).get("patterns", [])
            self.assertNotIn("db_password", patterns)


class TestClaudeContract(unittest.TestCase):
    def test_tool_response_redacts_and_emits_updated_output(self):
        stdin_raw = json.dumps(
            {"tool_name": "Bash", "tool_response": "export API_KEY=superSecretValue123"}
        )
        proc = subprocess.run(
            [sys.executable, HOOK],
            input=stdin_raw,
            capture_output=True,
            text=True,
            timeout=10,
        )
        self.assertEqual(proc.returncode, 0)
        result = json.loads(proc.stdout)
        self.assertNotIn("superSecretValue123", result["tool_result"])
        self.assertEqual(result["hookSpecificOutput"]["updatedToolOutput"], result["tool_result"])


class TestCodeIsNotASecret(unittest.TestCase):
    """Structural patterns must not rewrite ordinary source, docs or fixtures.

    Regression: ``token = request.headers.authorization`` used to come back as
    ``token = requ...`` — an agent then edits against text that is not on disk.
    """

    def test_source_assignment_expression_untouched(self):
        _, stdout, _ = run_hook(
            "const token = request.headers.authorization;\n"
            "const key = Object.keys(map)[0];\n"
            "const auth = HTTPBasicAuth(user, pw);\n"
            'canary_token = os.environ.get("X", "")\n'
            'run(key="tool_response")\n'
        )
        self.assertEqual(stdout, "")

    def test_quoted_literal_in_source_redacted(self):
        _, stdout, _ = run_hook('password = "S3cr3t-P4ss"\n')
        self.assertTrue(stdout)
        self.assertNotIn("S3cr3t-P4ss", json.loads(stdout)["tool_result"])

    def test_jwt_under_camel_case_key_redacted(self):
        value = "eyJhbGciOiJIUzI1NiJ9.abc123xyz"
        _, stdout, _ = run_hook(f'{{"accessToken": "{value}"}}')
        self.assertTrue(stdout)
        self.assertNotIn(value, json.loads(stdout)["tool_result"])

    def test_weak_names_need_credential_shaped_values(self):
        _, stdout, _ = run_hook(
            '{"key": "compound-command", "auth": "basic-header-style", "author": "wayne-sun-dev"}'
        )
        self.assertEqual(stdout, "")
        strong_value = "A1b2C3d4E5f6G7h8I9j0K1l2M3n4"  # gitleaks:allow
        _, stdout, _ = run_hook(f'{{"key": "{strong_value}"}}')
        self.assertTrue(stdout)

    def test_fixture_phrases_untouched(self):
        _, stdout, _ = run_hook(
            'DispatchSecret: "test-secret"\n'
            'Token: "ghs_policy_token"\n'
            'AccessToken: "cached-token"\n'
            'NextPageToken: "page-2-token"\n'
        )
        self.assertEqual(stdout, "")

    def test_names_about_a_secret_untouched(self):
        _, stdout, _ = run_hook(
            "TOKEN_URL=https://example.com/oauth/token\n"
            "KEY_ID=abcd1234efgh\n"
            "PUBLIC_KEY=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A\n"
            "GOOGLE_APPLICATION_CREDENTIALS=fullsend-local-credentials.json\n"
            'tokenUrl: "https://x.example/oauth"\n'
        )
        self.assertEqual(stdout, "")

    def test_env_style_values_redacted(self):
        env_lines = "DB_PASSWORD=Tr0ub4dor3xyz\nexport API_KEY=supersecretvalue\n"  # gitleaks:allow
        _, stdout, _ = run_hook(env_lines)
        self.assertTrue(stdout)
        result = json.loads(stdout)
        self.assertNotIn("Tr0ub4dor3xyz", result["tool_result"])
        self.assertNotIn("supersecretvalue", result["tool_result"])
        self.assertEqual(result["metadata"]["patterns"], ["env_secret", "env_secret"])

    def test_placeholders_untouched(self):
        _, stdout, _ = run_hook(
            "export GH_TOKEN=<your-token>\nPASSWORD=changeme\nSECRET=${SECRET}\n"
            "postgres://user:password@host/db\n"
        )
        self.assertEqual(stdout, "")


class TestEnvStyleWeakPasswords(unittest.TestCase):
    def test_lowercase_password_in_env_line_redacted(self):
        _, stdout, _ = run_hook("DB_PASSWORD=letmeinnow\n")
        self.assertTrue(stdout)
        self.assertNotIn("letmeinnow", json.loads(stdout)["tool_result"])

    def test_same_value_as_source_literal_untouched(self):
        _, stdout, _ = run_hook('password = "letmeinnow"\n')
        self.assertEqual(stdout, "")


class TestSecondReviewRound(unittest.TestCase):
    def test_keyword_arguments_untouched(self):
        _, stdout, _ = run_hook(
            "client = Client(token=fetchToken(), api_key=loadApiKey(cfg))\n"
            "secret=readSecretFile(path)\n"
            "run(key=cfg[0], auth=obj.attr)\n"
        )
        self.assertEqual(stdout, "")

    def test_system_qualified_names_redacted(self):
        value = "Tr0ub4dor3xyz"  # gitleaks:allow
        _, stdout, _ = run_hook(
            f"SOURCE_DB_PASSWORD={value}\nSTORE_PASSWORD={value}\nBACKEND_API_KEY={value}\n"
        )
        self.assertTrue(stdout)
        result = json.loads(stdout)
        self.assertNotIn(value, result["tool_result"])
        self.assertEqual(
            result["metadata"]["secrets_redacted"], 1
        )  # one value, replaced everywhere

    def test_id_token_is_a_token_but_key_id_is_not(self):
        jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0"
        _, stdout, _ = run_hook(f'{{"id_token": "{jwt}"}}\nID_TOKEN={jwt}\n')
        self.assertTrue(stdout)
        self.assertNotIn(jwt, json.loads(stdout)["tool_result"])
        _, stdout, _ = run_hook("KEY_ID=abcd1234efgh5678\nCLIENT_ID=1234567890-abcdefghij\n")
        self.assertEqual(stdout, "")

    def test_human_passwords_as_literals_redacted(self):
        _, stdout, _ = run_hook(
            '{"password": "Welcome-123"}\ndb_password = "secretpassword"\nJWT_SECRET=mysecretkey\n'
        )
        self.assertTrue(stdout)
        text = json.loads(stdout)["tool_result"]
        for leaked in ("Welcome-123", "secretpassword", "mysecretkey"):
            self.assertNotIn(leaked, text)

    def test_extension_and_placeholder_evasions_redacted(self):
        value = "Tr0ub4dor3xyz"  # gitleaks:allow
        dotted = "Tr0ub4dor.xyz"  # gitleaks:allow
        _, stdout, _ = run_hook(
            f"DB_PASSWORD={value}.txt\nDB_PASSWORD=your_{value}\nDB_PASSWORD={dotted}\n"
        )
        self.assertTrue(stdout)
        text = json.loads(stdout)["tool_result"]
        self.assertNotIn(value, text)
        self.assertNotIn(dotted, text)

    def test_db_password_bounded_and_idempotent(self):
        line = "DATABASE_URL=postgres://u:p4ssw0rd@db:5432/app # contact admin@example.com\n"
        _, stdout, _ = run_hook(line)
        first = json.loads(stdout)["tool_result"]
        self.assertIn("admin@example.com", first)
        self.assertNotIn("p4ssw0rd", first)
        _, stdout, _ = run_hook(first)
        self.assertEqual(stdout, "")


class TestSweepResidue(unittest.TestCase):
    def test_constant_names_and_pagination_and_short_prefixed_fakes_untouched(self):
        _, stdout, _ = run_hook(
            'SecretWIFProvider = "FULLSEND_GCP_WIF_PROVIDER"\n'
            'NextPageToken: "next-page"\n'
            'Token: "ghs_maskable"\n'
            '"token": "glpat-new"\n'
            'GitToken: "ghp_test123"\n'
            '{"key": "fullsend-triage-mr-1", "process_mode": "newest_first"}\n'
        )
        self.assertEqual(stdout, "")


class TestVerifyRound(unittest.TestCase):
    def test_real_vendor_tokens_with_known_prefixes_redacted(self):
        glpat = "glpat-" + "Ab3dEf9hIjKlMnOpQrSt"
        glrt = "glrt-" + "Zz9yXx8wVv7uTt6sRr5q"
        ya29 = "ya29." + "a0AfH6SMB" + "q1W2e3R4t5Y6u7I8o9P0a1S2d3F4g5"
        asia = "ASIA" + "ABCDEFGHIJ123456"
        _, stdout, _ = run_hook(
            f"GITLAB_TOKEN={glpat}\nRUNNER_TOKEN={glrt}\nACCESS_TOKEN={ya29}\nAWS_ACCESS_KEY_ID={asia}\n"
        )
        self.assertTrue(stdout)
        text = json.loads(stdout)["tool_result"]
        for leaked in (glpat, glrt, ya29, asia):
            self.assertNotIn(leaked, text)

    def test_service_account_token_redacted(self):
        # WIF-provisioned runs mint ya29.c.<blob> tokens; the one-char c
        # segment must not defeat the match (mirrors the Go redactor).
        ya29c = "ya29.c." + "b0Aaekm1K8sVq9dNfP2xJ3hT7wY5uZ4rQ6mE8oL1iC0aS"
        _, stdout, _ = run_hook(f"got token {ya29c} from the metadata server\n")
        self.assertTrue(stdout)
        self.assertNotIn(ya29c, json.loads(stdout)["tool_result"])

    def test_ghs_wrapped_jwt_fully_redacted(self):
        # GitHub's 2026 installation-token format wraps a JWT: the whole
        # token must mask, not just the ghs_ prefix and header segment
        # (mirrors the Go redactor's github_server_token).
        token = (
            "ghs_12345_"
            + "eyJhbGciOiJSUzI1NiJ9"
            + "."
            + "eyJzdWIiOiIxMjM0NTY3ODkwIn0"
            + "."
            + "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
        )
        _, stdout, _ = run_hook(f"Token: {token}\n")
        self.assertTrue(stdout)
        text = json.loads(stdout)["tool_result"]
        self.assertNotIn("eyJzdWIiOiIxMjM0NTY3ODkwIn0", text)
        self.assertNotIn("dBjftJeZ4CVP", text)

    def test_bare_jwt_redacted(self):
        # Segments concatenated so the fixture does not trip gitleaks.
        jwt = (
            "eyJhbGciOiJSUzI1NiJ9"
            + "."
            + "eyJzdWIiOiIxMjM0NTY3ODkwIn0"
            + "."
            + "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
        )
        _, stdout, _ = run_hook(f"curl output: {jwt}\n")
        self.assertTrue(stdout)
        self.assertNotIn(jwt, json.loads(stdout)["tool_result"])

    def test_short_prefixed_fakes_still_untouched(self):
        _, stdout, _ = run_hook(
            'Token: "ghs_maskable"\n"token": "glpat-new"\nGitToken: "ghp_test123"\n'
        )
        self.assertEqual(stdout, "")

    def test_bare_identifier_keyword_arguments_untouched(self):
        _, stdout, _ = run_hook(
            "client = Client(token=accessToken, api_key=apiKeyValue)\n"
            "retry(password=userPassword)\n"
        )
        self.assertEqual(stdout, "")

    def test_non_ascii_tail_does_not_hide_a_token(self):
        value = "Ab3dEf9hIjKlMnOpQrSt"  # gitleaks:allow
        _, stdout, _ = run_hook(f"API_TOKEN={value}\u00e9\n")
        self.assertTrue(stdout)
        self.assertNotIn(value, json.loads(stdout)["tool_result"])

    def test_constant_name_exemption_only_for_source_literals(self):
        _, stdout, _ = run_hook('SecretWIFProvider = "FULLSEND_GCP_WIF_PROVIDER"\n')
        self.assertEqual(stdout, "")
        env_value = "PROD_K3Y_9F86D081884C7D659A"  # gitleaks:allow
        _, stdout, _ = run_hook(f"SECRET_KEY={env_value}\n")
        self.assertTrue(stdout)

    def test_aws_pagination_tokens_untouched(self):
        continuation = "1a2B3c4D5e6F7g8H9i0J"  # gitleaks:allow
        next_token = "ZXlKaGJHY2lPaUpJVXpJMU5pSjk"  # gitleaks:allow
        page = f'{{"NextToken": "{next_token}", "ContinuationToken": "{continuation}"}}'
        _, stdout, _ = run_hook(page)
        self.assertEqual(stdout, "")


class TestBareJwtToolScope(unittest.TestCase):
    """A bare JWT has no fixture-shaped escape, so the pattern skips file
    content inside the checkout: a committed jwt.io example is not the live
    STS/OIDC token the pattern exists to catch, and masking it hands the
    agent text that is not in the file. Anything outside the checkout — the
    runner's own token file included — still masks. The layout mirrors the
    sandbox: a checkout with .git beside the runner's token file."""

    # Segments concatenated so the fixture does not trip gitleaks.
    JWT = (
        "eyJhbGciOiJSUzI1NiJ9"
        + "."
        + "eyJzdWIiOiIxMjM0NTY3ODkwIn0"
        + "."
        + "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
    )

    def setUp(self):
        import tempfile

        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = os.path.realpath(tmp.name)
        self.ws = os.path.join(self.tmp, "ws")
        self.repo = os.path.join(self.ws, "repo")
        os.makedirs(os.path.join(self.repo, ".git"))
        os.makedirs(os.path.join(self.repo, "pkg"))
        os.makedirs(os.path.join(self.repo, "internal", "cli"))
        self.fixture = os.path.join(self.repo, "pkg", "x_test.go")
        Path(self.fixture).write_text("x")
        self.token = os.path.join(self.ws, ".gcp-oidc-token")
        Path(self.token).write_text("x")
        # The tempdir stands in for /sandbox/workspace: the checkout must be
        # a proper subdirectory of it, so the boundary is bound per test.
        import secret_redact_posttool as sr

        self._orig_workspace = sr.SANDBOX_WORKSPACE
        sr.SANDBOX_WORKSPACE = self.ws
        self.addCleanup(setattr, sr, "SANDBOX_WORKSPACE", self._orig_workspace)
        self.assertIsNone(
            sr._checkout_root(self.ws, self.ws), "the workspace itself is never a root"
        )

    def _hook_input(self, tool_name, path=None, *, cwd=None, tool_input=None) -> dict:
        body: dict = {"tool_name": tool_name, "cwd": self.repo if cwd is None else cwd}
        if tool_input is not None:
            body["tool_input"] = tool_input
        elif path is not None:
            body["tool_input"] = {"file_path": path}
        return body

    def test_skip_for_checkout_paths(self):
        import secret_redact_posttool as sr

        for tool in ("Read", "Edit", "MultiEdit", "Write"):
            with self.subTest(tool=tool):
                self.assertEqual(sr.content_skips(self._hook_input(tool, self.fixture)), {"jwt"})
        for tool in ("NotebookEdit", "NotebookRead"):
            with self.subTest(tool=tool):
                body = self._hook_input(tool, tool_input={"notebook_path": self.fixture})
                self.assertEqual(sr.content_skips(body), {"jwt"})
        pkg = os.path.join(self.repo, "pkg")
        grep = self._hook_input("Grep", tool_input={"pattern": "eyJ", "path": pkg})
        self.assertEqual(sr.content_skips(grep), {"jwt"})
        self.assertEqual(sr.content_skips(self._hook_input("Read", "pkg/x_test.go")), {"jwt"})
        grep = self._hook_input("Grep", tool_input={"pattern": "eyJ"})
        self.assertEqual(sr.content_skips(grep), {"jwt"})

    def test_no_skip_outside_checkout(self):
        import secret_redact_posttool as sr

        plain = os.path.join(self.ws, "plain")
        os.makedirs(plain)
        Path(os.path.join(plain, "f.go")).write_text("x")
        cases = [
            self._hook_input("Read", self.token),
            self._hook_input("Grep", tool_input={"pattern": "eyJ", "path": self.ws}),
            # No .git anywhere above cwd: no checkout, no skip.
            self._hook_input("Read", os.path.join(plain, "f.go"), cwd=plain),
            self._hook_input("Bash", tool_input={"command": "cat x"}),
            self._hook_input("WebFetch", tool_input={"url": "https://x"}),
        ]
        for body in cases:
            with self.subTest(body=body):
                self.assertEqual(sr.content_skips(body), frozenset())

    def test_traversal_and_tilde_never_skip(self):
        # The runtime opens the normalized path; realpath would follow a
        # committed symlink first, so link/../../token lands outside while
        # looking inside. No fixture needs '..', so refuse it outright.
        import secret_redact_posttool as sr

        dotted = os.path.join(self.repo, "pkg", "..", "pkg", "x_test.go")
        for path in (dotted, "pkg/../pkg/x_test.go", "~/x_test.go"):
            with self.subTest(path=path):
                self.assertEqual(sr.content_skips(self._hook_input("Read", path)), frozenset())

    def test_symlink_out_of_checkout_is_resolved(self):
        import secret_redact_posttool as sr

        link = os.path.join(self.repo, "token.txt")
        os.symlink(self.token, link)
        self.assertEqual(sr.content_skips(self._hook_input("Read", link)), frozenset())
        deep = os.path.join(self.repo, "link")
        os.symlink("a/b", deep)
        escape = f"{deep}/../../.gcp-oidc-token"
        self.assertEqual(sr.content_skips(self._hook_input("Read", escape)), frozenset())
        self.assertEqual(sr.content_skips(self._hook_input("Read", self.fixture)), {"jwt"})

    def test_symlinked_cwd_never_reaches_outside(self):
        # A cd through a symlink resolves to the real directory: outward,
        # nothing there holds .git, so no skip whatever the path; inward, the
        # checkout is found.
        import secret_redact_posttool as sr

        ws_link = os.path.join(self.repo, "ws")
        os.symlink(self.ws, ws_link)
        cases = [
            self._hook_input("Read", self.token, cwd=ws_link),
            self._hook_input("Read", ".gcp-oidc-token", cwd=ws_link),
            self._hook_input("Grep", cwd=ws_link, tool_input={"pattern": "eyJ"}),
        ]
        up = os.path.join(self.repo, "up")
        os.symlink("..", up)
        cases.append(self._hook_input("Read", self.token, cwd=up))
        for body in cases:
            with self.subTest(body=body):
                self.assertEqual(sr.content_skips(body), frozenset())
        inward = os.path.join(self.ws, "inward")
        os.symlink(os.path.join(self.repo, "internal"), inward)
        self.assertEqual(
            sr.content_skips(self._hook_input("Read", self.fixture, cwd=inward)), {"jwt"}
        )

    def test_forged_git_above_the_checkout_never_becomes_the_root(self):
        # An agent can create a .git entry at the workspace level and get
        # its cwd to resolve there (a symlink inside the checkout, or a plain
        # cd); the checkout must be a proper subdirectory of the sandbox
        # workspace, so that entry can never widen the root to the runner's
        # token file.
        import secret_redact_posttool as sr

        os.makedirs(os.path.join(self.ws, ".git"))
        ws_link = os.path.join(self.repo, "ws")
        os.symlink(self.ws, ws_link)
        for cwd in (ws_link, self.ws):
            with self.subTest(cwd=cwd):
                self.assertEqual(
                    sr.content_skips(self._hook_input("Read", self.token, cwd=cwd)), frozenset()
                )
                self.assertEqual(
                    sr.content_skips(self._hook_input("Read", ".gcp-oidc-token", cwd=cwd)),
                    frozenset(),
                )
                grep = self._hook_input("Grep", cwd=cwd, tool_input={"pattern": "eyJ"})
                self.assertEqual(sr.content_skips(grep), frozenset())
        # A .git planted ABOVE the workspace is out of bounds the same way —
        # including for a cwd inside the workspace but outside any checkout,
        # where an unbounded walk would find it. The workspace-level plant is
        # removed first so the one above is genuinely the nearest ancestor.
        os.rmdir(os.path.join(self.ws, ".git"))
        os.makedirs(os.path.join(self.tmp, ".git"))
        plain = os.path.join(self.ws, "plain")
        os.makedirs(plain)
        for cwd in (self.ws, plain):
            with self.subTest(cwd=cwd):
                self.assertEqual(
                    sr.content_skips(self._hook_input("Read", self.token, cwd=cwd)), frozenset()
                )
        # The real checkout, a proper subdirectory of the workspace, still skips.
        self.assertEqual(sr.content_skips(self._hook_input("Read", self.fixture)), {"jwt"})

    def test_runtime_rewritten_path_forms_never_skip(self):
        # pi strips a leading '@', turns a file:// URL into a path and expands
        # '~' before opening, while its adapter forwards the raw argument; the
        # hook cannot see the rewrite, so those forms never skip — a rewritten
        # form can name the token beside the checkout as easily as a fixture.
        import secret_redact_posttool as sr

        for path in (
            "@" + self.token,
            "@../.gcp-oidc-token",
            "@~/.gcp-oidc-token",
            "file://" + self.token,
            "FILE://" + self.token,
            "@" + self.fixture,
            "file://" + self.fixture,
        ):
            with self.subTest(path=path):
                read = {"tool_name": "Read", "cwd": self.repo, "tool_input": {"file_path": path}}
                grep = {"tool_name": "Grep", "cwd": self.repo, "tool_input": {"path": path}}
                self.assertEqual(sr.content_skips(read), frozenset())
                self.assertEqual(sr.content_skips(grep), frozenset())

    def test_workspace_boundary_is_resolved_before_comparison(self):
        # The boundary is compared in resolved space, like cwd, so a symlink
        # path for the workspace still finds the checkout below it.
        import secret_redact_posttool as sr

        link = os.path.join(self.tmp, "wslink")
        os.symlink(self.ws, link)
        sr.SANDBOX_WORKSPACE = link
        read = {"tool_name": "Read", "cwd": self.repo, "tool_input": {"file_path": self.fixture}}
        self.assertEqual(sr.content_skips(read), {"jwt"})

    def test_boundary_comes_from_argv_never_from_the_environment(self):
        # Claude Code applies a checkout's .claude/settings.json env block to
        # hook processes over the launch environment, so the boundary is never
        # taken from there: with only the environment naming this workspace,
        # the checkout is not below the real boundary and the JWT masks; the
        # command-line seam is what moves it.
        import json
        import subprocess
        import sys

        import secret_redact_posttool as sr

        body = {
            "tool_name": "Read",
            "cwd": self.repo,
            "tool_input": {"file_path": self.fixture},
            "tool_response": self.JWT,
        }
        run = lambda *extra, env: subprocess.run(  # noqa: E731
            [sys.executable, sr.__file__, *extra],
            input=json.dumps(body),
            capture_output=True,
            text=True,
            timeout=10,
            env=env,
        )
        env_only = run(env={**os.environ, "FULLSEND_SANDBOX_WORKSPACE": self.ws})
        self.assertEqual(env_only.returncode, 0, env_only.stderr)
        self.assertNotIn(self.JWT, env_only.stdout)
        self.assertIn("updatedToolOutput", env_only.stdout)
        flagged = run("--sandbox-workspace=" + self.ws, env=os.environ.copy())
        self.assertEqual(flagged.returncode, 0, flagged.stderr)
        self.assertEqual(flagged.stdout.strip(), "")
        # A relative value that WOULD resolve to this workspace from the
        # process's cwd is still ignored, so the JWT masks.
        relative = subprocess.run(
            [sys.executable, sr.__file__, "--sandbox-workspace=" + os.path.basename(self.ws)],
            input=json.dumps(body),
            capture_output=True,
            text=True,
            timeout=10,
            env=os.environ.copy(),
            cwd=self.tmp,
        )
        self.assertEqual(relative.returncode, 0, relative.stderr)
        self.assertIn("updatedToolOutput", relative.stdout)

    def test_sandbox_workspace_from_argv(self):
        import secret_redact_posttool as sr

        self.assertEqual(
            sr.sandbox_workspace_from_argv(["--sandbox-workspace=" + self.ws]), self.ws
        )
        self.assertIsNone(sr.sandbox_workspace_from_argv(["--sandbox-workspace=relative/ws"]))
        self.assertIsNone(sr.sandbox_workspace_from_argv(["--sandbox-workspace", self.ws]))
        self.assertIsNone(sr.sandbox_workspace_from_argv([]))

    def test_checkout_outside_the_workspace_never_skips(self):
        # A .git-bearing directory that is not under the sandbox workspace is
        # not a checkout the skip may trust, however it was reached.
        import tempfile

        import secret_redact_posttool as sr

        with tempfile.TemporaryDirectory() as other:
            repo = os.path.join(os.path.realpath(other), "repo")
            os.makedirs(os.path.join(repo, ".git"))
            f = os.path.join(repo, "x_test.go")
            Path(f).write_text("x")
            self.assertEqual(sr.content_skips(self._hook_input("Read", f, cwd=repo)), frozenset())

    def test_root_is_nearest_git_ancestor_of_cwd(self):
        # cwd follows the agent's persisted cd; the checkout is still the root.
        import secret_redact_posttool as sr

        cwd = os.path.join(self.repo, "internal", "cli")
        self.assertEqual(sr.content_skips(self._hook_input("Read", self.fixture, cwd=cwd)), {"jwt"})
        self.assertEqual(
            sr.content_skips(self._hook_input("Read", self.token, cwd=cwd)), frozenset()
        )
        grep = self._hook_input("Grep", cwd=cwd, tool_input={"pattern": "eyJ", "path": self.repo})
        self.assertEqual(sr.content_skips(grep), {"jwt"})
        # A submodule cwd narrows the root to the submodule: its own files
        # skip, superproject fixtures mask again — the safe direction.
        dep = os.path.join(self.repo, "vendor", "dep")
        os.makedirs(dep)
        Path(os.path.join(dep, ".git")).write_text("gitdir: ../../.git/modules/dep\n")
        dep_file = os.path.join(dep, "x.go")
        Path(dep_file).write_text("x")
        self.assertEqual(sr.content_skips(self._hook_input("Read", dep_file, cwd=dep)), {"jwt"})
        self.assertEqual(
            sr.content_skips(self._hook_input("Read", self.fixture, cwd=dep)), frozenset()
        )

    def test_grep_uses_its_own_path_key(self):
        import secret_redact_posttool as sr

        stray = {"pattern": "eyJ", "path": self.ws, "file_path": self.fixture}
        self.assertEqual(sr.content_skips(self._hook_input("Grep", tool_input=stray)), frozenset())
        self.assertEqual(sr.content_skips(self._hook_input("Grep", tool_input={})), {"jwt"})
        self.assertEqual(sr.content_skips({"tool_name": "Grep", "cwd": self.repo}), frozenset())

    def test_malformed_input_means_mask_not_error(self):
        import secret_redact_posttool as sr

        read = {"file_path": self.fixture}
        cases = [
            {"tool_name": ["Read"], "cwd": self.repo, "tool_input": read},
            {"tool_name": "Read", "tool_input": read},
            {"tool_name": "Read", "cwd": None, "tool_input": read},
            {"tool_name": "Read", "cwd": "repo", "tool_input": read},
            {"tool_name": "Read", "cwd": self.repo},
            {"tool_name": "Read", "cwd": self.repo, "tool_input": "not json"},
            {"tool_name": "Read", "cwd": self.repo, "tool_input": {"file_path": [self.fixture]}},
            {"tool_name": "Read", "cwd": self.repo, "tool_input": {"file_path": ""}},
            {
                "tool_name": "Read",
                "cwd": self.repo,
                "tool_input": {"file_path": self.fixture + "\x00"},
            },
        ]
        for body in cases:
            with self.subTest(body=body):
                self.assertEqual(sr.content_skips(body), frozenset())
        text, findings = sr.redact_text(f"tok {self.JWT}\n", skip=sr.content_skips(cases[0]))
        self.assertNotIn(self.JWT, text)
        self.assertEqual([f["pattern"] for f in findings], ["jwt"])

    def test_tool_input_as_json_string(self):
        import secret_redact_posttool as sr

        body = {
            "tool_name": "Read",
            "cwd": self.repo,
            "tool_input": json.dumps({"file_path": self.fixture}),
        }
        self.assertEqual(sr.content_skips(body), {"jwt"})

    def test_skip_is_jwt_only(self):
        import secret_redact_posttool as sr

        self.assertEqual(sr._CHECKOUT_SKIPS, frozenset({"jwt"}))
        ya29c = "ya29.c." + "b0Aaekm1K8sVq9dNfP2xJ3hT7wY5uZ4rQ6mE8oL1iC0aS"
        ghs = "ghs_12345_" + self.JWT
        text, findings = sr.redact_text(
            f"a = {ya29c}\nb = {ghs}\nc = {self.JWT}\n", skip=frozenset({"jwt"})
        )
        self.assertNotIn(ya29c, text)
        self.assertNotIn(ghs, text)
        self.assertIn(self.JWT, text)
        self.assertEqual(
            sorted(f["pattern"] for f in findings), ["github_server_token", "google_oauth_token"]
        )

    def test_named_assignment_still_masked_by_structural_pattern(self):
        # The skip covers the context-free pattern only: an assignment whose
        # name says it is a token is masked by env_secret on every tool, as
        # before this pattern existed.
        import secret_redact_posttool as sr

        text, findings = sr.redact_text(f'var testToken = "{self.JWT}"\n', skip=frozenset({"jwt"}))
        self.assertNotIn(self.JWT, text)
        self.assertEqual([f["pattern"] for f in findings], ["env_secret"])

    def test_script_honours_checkout_scope(self):
        def run(path: str, tool_result: str) -> str:
            body = {
                "tool_name": "Read",
                "cwd": self.repo,
                "tool_input": {"file_path": path},
                "tool_result": tool_result,
            }
            proc = subprocess.run(
                [sys.executable, HOOK, "--sandbox-workspace=" + self.ws],
                input=json.dumps(body),
                capture_output=True,
                text=True,
                timeout=10,
                env=os.environ.copy(),
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            return proc.stdout

        fixture = f'\t{{name: "valid", input: "{self.JWT}"}},\n'
        self.assertEqual(run(self.fixture, fixture), "")
        stdout = run(self.token, f"{self.JWT}\n")
        self.assertTrue(stdout)
        self.assertNotIn(self.JWT, json.loads(stdout)["tool_result"])


class TestPaginationVsSecretNames(unittest.TestCase):
    def test_pagination_words_only_veto_token_names(self):
        import secret_redact_posttool as sr

        for name in ("NextToken", "ContinuationToken", "nextPageToken", "cursor"):
            self.assertIsNone(sr.name_strength(name), name)
        for name in ("NEXT_SECRET", "PAGE_PASSWORD", "next_credential"):
            self.assertEqual("strong", sr.name_strength(name), name)


class TestQualifiedKeyUnderPaginationWords(unittest.TestCase):
    def test_api_key_survives_a_pagination_word(self):
        import secret_redact_posttool as sr

        for name in ("NEXT_API_KEY", "PAGE_ACCESS_KEY", "next_secret"):
            self.assertEqual("strong", sr.name_strength(name), name)
        for name in ("NextToken", "nextPageToken", "PAGE_TOKEN", "NEXT_PUBLIC_API_KEY"):
            self.assertIsNone(sr.name_strength(name), name)

    def test_gitlab_prefixed_fakes_are_left_alone(self):
        fixtures = '"token": "glptt-new"\nToken: "gldt-example"\n"runner": "glrt-test"\n'
        _, stdout, _ = run_hook(fixtures)
        self.assertEqual(stdout, "")


if __name__ == "__main__":
    unittest.main()
