#!/usr/bin/env python3
"""Unit tests for secret_redact_posttool.py hook."""

import json
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


if __name__ == "__main__":
    unittest.main()
