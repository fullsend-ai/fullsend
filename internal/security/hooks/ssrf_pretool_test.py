"""Tests for ssrf_pretool.py PreToolUse hook."""

from __future__ import annotations

import importlib.util
import json
import os
import socket
import subprocess
import sys
from unittest import mock

import pytest

HOOK_PATH = os.path.join(os.path.dirname(__file__), "ssrf_pretool.py")


def _load_hook_module():
    spec = importlib.util.spec_from_file_location("ssrf_pretool", HOOK_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture()
def hook():
    return _load_hook_module()


# ---------------------------------------------------------------------------
# _is_in_text_pattern_context tests
# ---------------------------------------------------------------------------


class TestIsInTextPatternContext:
    """Unit tests for the text-manipulation context detector."""

    def test_sed_pipe_delimiter(self, hook):
        cmd = "sed 's|https://github.com/||'"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_slash_delimiter(self, hook):
        # URL won't fully match through escaped slashes, but the prefix
        # detection should still work for any URL-shaped match that does land.
        cmd = "sed 's/https://example.com//'"
        matches = list(hook.URL_PATTERN.finditer(cmd))
        assert matches, "URL_PATTERN should match"
        assert hook._is_in_text_pattern_context(cmd, matches[0].start())

    def test_sed_with_flags(self, hook):
        cmd = "sed -e 's|https://github.com/||g'"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_in_pipeline(self, hook):
        cmd = "echo \"$URL\" | sed 's|https://github.com/||; s|/issues/.*||'"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_second_substitution(self, hook):
        cmd = "sed 's|foo|bar|; s|https://api.example.com/||'"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_single_quoted(self, hook):
        cmd = "grep 'https://github.com/owner' file.txt"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_double_quoted(self, hook):
        cmd = 'grep "https://github.com/owner" file.txt'
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_with_flags(self, hook):
        cmd = "grep -rn 'https://github.com/' src/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_egrep_quoted(self, hook):
        cmd = "egrep 'https://example.com/path' logfile"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_awk_quoted(self, hook):
        cmd = "awk '/https://example.com/' access.log"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_curl_url_not_in_context(self, hook):
        cmd = "curl https://api.github.com/repos/owner/repo"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_wget_url_not_in_context(self, hook):
        cmd = "wget https://example.com/file.tar.gz"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_bare_url_not_in_context(self, hook):
        cmd = "https://example.com/something"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_curl_dns_servers_not_in_context(self, hook):
        """Prefix ending in 's=' (--dns-servers=) must not trigger sed bypass."""
        cmd = "curl --dns-servers=https://169.254.169.254/latest/meta-data/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_curl_pass_not_in_context(self, hook):
        """Prefix ending in 's=' (--pass=) must not trigger sed bypass."""
        cmd = "curl --pass=https://169.254.169.254/latest/meta-data/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_variable_assignment_not_in_context(self, hook):
        """Variable assignment like 'process=URL' must not trigger sed bypass."""
        cmd = "process=https://169.254.169.254/latest/meta-data/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_replacement_url_not_in_context(self, hook):
        """URL in sed replacement field must not be exempt."""
        cmd = "sed 's|items|https://evil.com/payload|'"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_notgrep_not_in_context(self, hook):
        """Binary names ending in 'grep' must not trigger grep bypass."""
        cmd = "notgrep 'https://169.254.169.254/' file.txt"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_myawk_not_in_context(self, hook):
        """Binary names ending in 'awk' must not trigger awk bypass."""
        cmd = "myawk '/https://169.254.169.254/' access.log"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_cross_segment_semicolon_not_in_context(self, hook):
        """sed in one statement must not exempt a URL in a later statement."""
        cmd = "echo sed 's|'; curl https://169.254.169.254/latest/meta-data/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_cross_segment_and_not_in_context(self, hook):
        """sed in one statement must not exempt a URL after &&."""
        cmd = "echo sed 's/' && curl https://evil.internal/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_cross_segment_pipe_not_in_context(self, hook):
        """URL in a curl segment after a pipe from a sed-mentioning segment."""
        cmd = "echo sed | curl https://169.254.169.254/latest/meta-data/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_pipe_to_xargs_curl_not_in_context(self, hook):
        """grep -o URL piped to xargs curl is a network target."""
        cmd = "grep -oP 'https://169.254.169.254/latest/' file | xargs curl"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_pipe_to_wget_not_in_context(self, hook):
        """grep URL piped to wget is a network target."""
        cmd = "grep -o 'https://internal.host/path' log | wget -i -"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_pipe_to_sort_still_in_context(self, hook):
        """grep URL piped to non-network command is still exempt."""
        cmd = "grep 'https://github.com/owner' src/ | sort"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_no_pipe_still_in_context(self, hook):
        """grep URL without pipe is still exempt (no downstream sink)."""
        cmd = "grep 'https://github.com/owner' file.txt"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    # --- Nested shell / command-substitution / redirection bypass tests ---

    def test_bash_c_grep_pipe_not_in_context(self, hook):
        """bash -c creates a second shell; URL must not be exempted."""
        cmd = "bash -c \"grep 'https://169.254.169.254/latest/' f | xargs curl\""
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sh_c_grep_not_in_context(self, hook):
        """sh -c creates a second shell; URL must not be exempted."""
        cmd = "sh -c \"grep 'https://169.254.169.254/' f\""
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_eval_grep_not_in_context(self, hook):
        """eval re-parses the string; URL must not be exempted."""
        cmd = "eval \"grep 'https://169.254.169.254/' f\""
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_bash_c_with_interposed_flags_not_in_context(self, hook):
        """Flags between the shell name and -c must not defeat reentry detection."""
        for cmd in (
            "bash -x -c \"grep 'https://169.254.169.254/latest/' f\"",
            "bash --norc -c \"grep 'https://169.254.169.254/latest/' f\"",
            "sh -l -c \"grep 'https://169.254.169.254/latest/' f\"",
        ):
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_sed_command_substitution_not_in_context(self, hook):
        """Command substitution inside sed pattern executes the URL."""
        cmd = 'sed "s/$(curl https://169.254.169.254/latest/meta-data/)/replacement/" file'
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_backtick_substitution_not_in_context(self, hook):
        """Backtick substitution inside sed pattern executes the URL."""
        cmd = 'sed "s/`curl https://169.254.169.254/latest/`/replacement/" file'
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_process_substitution_not_in_context(self, hook):
        """Process substitution <(...) runs curl before sed; URL must not be exempted."""
        cmd = "sed 's@' <(curl https://169.254.169.254/latest/)"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_output_process_substitution_not_in_context(self, hook):
        """Output process substitution >(...) also runs a nested command."""
        cmd = "sed 's@' >(curl https://169.254.169.254/latest/)"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_dash_f_process_substitution_not_in_context(self, hook):
        """sed -f <(...) reads a script from a nested command."""
        cmd = "sed -e 's@' -f <(curl https://169.254.169.254/latest/)"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_process_substitution_not_in_context(self, hook):
        """grep reading from <(...) must not exempt the nested command's URL."""
        cmd = "grep -o 'x' <(curl https://169.254.169.254/latest/)"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_background_operator_ends_sed_segment(self, hook):
        """A single & starts a new statement; the following curl URL is not sed context."""
        cmd = "sed 's@' f & curl https://169.254.169.254/latest/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_background_operator_ends_grep_segment(self, hook):
        """A single & separates grep from a following network command."""
        cmd = "grep -o 'x' f & wget https://169.254.169.254/latest/"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_variable_expansion_still_in_context(self, hook):
        """${var} is expansion, not command substitution — sed pattern stays exempt."""
        cmd = 'sed "s@${x}https://github.com/@y@" f'
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_literal_substitution_marker_in_context(self, hook):
        """$( inside single quotes is literal text for sed, not a subshell."""
        cmd = "sed 's|$(x)https://github.com/|y|' f"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_awk_system_call_not_in_context(self, hook):
        """awk's system() runs a shell from inside the pattern argument."""
        cmd = "awk '/https://169.254.169.254/latest/ {system(\"curl \"$0)}' f"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_awk_command_pipe_not_in_context(self, hook):
        """awk can pipe output straight into a command."""
        cmd = "awk '/https://169.254.169.254/ {print | \"curl -d @- x\"}' f"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_execute_flag_not_in_context(self, hook):
        """GNU sed's e flag executes the replacement as a shell command."""
        cmd = "sed 's@x@curl https://169.254.169.254/@e' f"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_script_filename_is_not_an_interpreter(self, hook):
        """A script's extension must not read as an interpreter and block the URL."""
        for target in ("install.sh", "deploy.bash", "scripts/setup.sh", "a.zsh", "~/.bashrc"):
            cmd = f"sed 's|https://github.com/||' {target}"
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_absolute_path_shell_still_detected(self, hook):
        """/bin/sh and /usr/bin/bash must still count as interpreters."""
        for shell in ("/bin/sh", "/usr/bin/bash"):
            cmd = f"grep -o 'https://169.254.169.254/latest/' file | {shell}"
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_sed_write_and_execute_flags_not_in_context(self, hook):
        """sed can write a file or run a command with no pipe or redirection."""
        for cmd in (
            "sed -n 's|http://169.254.169.254/|&|w /tmp/u' file",
            "sed -n 's|http://169.254.169.254/|&|W /tmp/u' file",
            "sed 's|http://169.254.169.254/||e' file",
            "sed -n '/http://169.254.169.254//w /tmp/u' file",
            "sed 's|http://169.254.169.254/|&|gw /tmp/u' file",
            "sed 's|http://169.254.169.254/|&|; w /tmp/u' file",
        ):
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_ordinary_sed_flags_still_in_context(self, hook):
        """Harmless substitution flags and paths must keep the exemption."""
        for cmd in (
            "sed 's|https://github.com/||g' f",
            "sed -n 's|https://github.com/||p' f",
            "sed 's|https://github.com/||I' f",
            "sed 's|https://web.example/||' f",
        ):
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_compound_grouping_does_not_end_pipeline(self, hook):
        """A ';' inside { }, ( ), do/done or then/fi is not a real pipeline end."""
        for cmd in (
            "{ grep 'https://169.254.169.254/latest/' file; } | xargs curl",
            "( grep 'https://169.254.169.254/latest/' file; ) | xargs curl",
            "for i in 1; do grep 'https://169.254.169.254/latest/' file; done | xargs curl",
            "if true; then grep 'https://169.254.169.254/latest/' file; fi | xargs curl",
            "{ grep -o 'https://169.254.169.254/latest/' file; } | tee /tmp/x",
        ):
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_loop_without_downstream_pipe_still_in_context(self, hook):
        """A loop that pipes nowhere keeps the exemption."""
        cmd = "for f in *; do sed 's|https://github.com/||' $f; done"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_replacement_reproducing_match_not_in_context(self, hook):
        """sed's & and \\1 put the URL back on stdout, so downstream matters."""
        for cmd in (
            "sed 's,https://169.254.169.254/latest/,&,' file | xargs curl",
            "sed 's,\\(https://169.254.169.254/latest/\\),\\1,' file | xargs curl",
            "sed 's,https://169.254.169.254/latest/,&,' file > /tmp/x",
            "sed 's,https://169.254.169.254/latest/,&,' file | tee /tmp/x",
        ):
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_allowlisted_command_with_output_flag_not_in_context(self, hook):
        """An allowlisted binary's own -o/--output still persists the URL."""
        for consumer in ("sort -o /tmp/x", "sort --output=/tmp/x"):
            cmd = f"grep -o 'https://169.254.169.254/latest/' file | {consumer}"
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_sed_piped_to_pure_filter_still_in_context(self, hook):
        """An ordinary sed pipeline into a pure filter keeps the exemption."""
        cmd = "echo \"$U\" | sed 's|https://github.com/||' | tr -d ' '"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_piped_to_persisting_command_not_in_context(self, hook):
        """Any consumer that persists grep output disqualifies the exemption."""
        for consumer in ("tee /tmp/u", "dd of=/tmp/u", "cp /dev/stdin /tmp/u", "split - /tmp/u"):
            cmd = f"grep -o 'https://169.254.169.254/latest/' file | {consumer}"
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_grep_pipe_through_viewer_into_danger_not_in_context(self, hook):
        """A pure viewer must not launder the output into an unsafe stage."""
        for cmd in (
            "grep -o 'https://169.254.169.254/latest/' f | sort | xargs curl",
            "grep -o 'https://169.254.169.254/latest/' f | tail -1 | tee /tmp/u",
            "grep -o 'https://169.254.169.254/latest/' f | sort > /tmp/u",
        ):
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_grep_pipe_to_unknown_command_not_in_context(self, hook):
        """An unrecognised consumer fails safe rather than exempting."""
        cmd = "grep -o 'https://169.254.169.254/latest/' f | somenewtool"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_pipe_through_pure_viewers_still_in_context(self, hook):
        """Chained pure viewers keep the exemption."""
        cmd = "grep 'https://github.com/owner' src/ | sort | uniq | wc -l"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_piped_to_shell_not_in_context(self, hook):
        """Piping grep output into a shell hands it arbitrary execution."""
        for shell in ("bash", "sh", "dash", "zsh", "ksh"):
            cmd = f"grep -o 'https://169.254.169.254/latest/' file | {shell}"
            m = list(hook.URL_PATTERN.finditer(cmd))[0]
            assert not hook._is_in_text_pattern_context(cmd, m.start()), cmd

    def test_grep_redirect_to_file_not_in_context(self, hook):
        """grep URL with output redirection must not be exempted."""
        cmd = "grep -o 'https://169.254.169.254/' file > /tmp/urls"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_append_redirect_not_in_context(self, hook):
        """grep URL with >> redirection must not be exempted."""
        cmd = "grep -o 'https://169.254.169.254/' file >> /tmp/urls"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_grep_pipe_to_python_not_in_context(self, hook):
        """grep URL piped to python is a network target."""
        cmd = "grep -o 'https://169.254.169.254/' file | python3 -c 'import urllib.request'"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_sed_compact_e_flag(self, hook):
        """Compact GNU sed form sed -es#URL## should still be detected."""
        cmd = "sed -es#https://github.com/##"
        matches = list(hook.URL_PATTERN.finditer(cmd))
        assert matches, "URL_PATTERN should match"
        assert hook._is_in_text_pattern_context(cmd, matches[0].start())

    # --- Command substitution wrapping grep/awk bypass tests ---

    def test_curl_dollar_paren_grep_not_in_context(self, hook):
        """grep inside $() feeding curl — URL must not be exempted."""
        cmd = "curl $(grep -o 'https://example.com/path' /some/file)"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_curl_backtick_grep_not_in_context(self, hook):
        """grep inside backticks feeding curl — URL must not be exempted."""
        cmd = "curl `grep -o 'https://example.com/path' /some/file`"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_wget_dollar_paren_awk_not_in_context(self, hook):
        """awk inside $() feeding wget — URL must not be exempted."""
        cmd = "wget $(awk '/https://example.com/' access.log)"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_nested_dollar_paren_grep_not_in_context(self, hook):
        """Nested $() around grep — URL must not be exempted."""
        cmd = "echo $(curl $(grep -o 'https://example.com/' file))"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_dollar_paren_grep_in_dquotes_not_in_context(self, hook):
        """$() inside double quotes is still active — URL must not be exempted."""
        cmd = "curl \"$(grep -o 'https://example.com/path' file)\""
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert not hook._is_in_text_pattern_context(cmd, m.start())

    def test_literal_dollar_paren_in_squotes_still_in_context(self, hook):
        """$( inside single quotes is literal — grep should still be exempt."""
        cmd = "echo '$(not_a_subshell)' && grep 'https://github.com/owner' file.txt"
        m = list(hook.URL_PATTERN.finditer(cmd))[0]
        assert hook._is_in_text_pattern_context(cmd, m.start())


# ---------------------------------------------------------------------------
# _extract_network_urls tests
# ---------------------------------------------------------------------------


class TestExtractNetworkUrls:
    """Unit tests for network-relevant URL extraction."""

    def test_sed_url_excluded(self, hook):
        cmd = "sed 's|https://github.com/||'"
        assert hook._extract_network_urls(cmd) == []

    def test_curl_url_included(self, hook):
        cmd = "curl https://api.github.com/repos"
        urls = hook._extract_network_urls(cmd)
        assert urls == ["https://api.github.com/repos"]

    def test_mixed_sed_and_curl(self, hook):
        """sed URL excluded, curl URL still validated."""
        cmd = "echo \"$URL\" | sed 's|https://github.com/||' && curl https://api.github.com/repos"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "api.github.com" in urls[0]

    def test_multiple_sed_substitutions(self, hook):
        cmd = "sed 's|https://github.com/||; s|https://example.com/||'"
        assert hook._extract_network_urls(cmd) == []

    def test_no_urls(self, hook):
        cmd = "ls -la /tmp"
        assert hook._extract_network_urls(cmd) == []

    def test_grep_url_excluded(self, hook):
        cmd = "grep 'https://github.com/owner' src/"
        assert hook._extract_network_urls(cmd) == []

    def test_curl_dns_servers_url_included(self, hook):
        """URL after --dns-servers= must not be dropped by sed bypass."""
        cmd = "curl --dns-servers=https://169.254.169.254/latest/meta-data/"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]

    def test_sed_replacement_url_included(self, hook):
        """URL in sed replacement field is still a candidate for validation."""
        cmd = "sed 's|items|https://evil.com/payload|'"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "evil.com" in urls[0]

    def test_sed_cross_segment_url_included(self, hook):
        """sed in one statement must not suppress a URL in a later statement."""
        cmd = "echo sed 's|'; curl https://169.254.169.254/latest/meta-data/"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]

    def test_grep_pipe_to_xargs_curl_included(self, hook):
        """grep -o URL piped to xargs curl must not be dropped."""
        cmd = "grep -oP 'https://169.254.169.254/latest/' file | xargs curl"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]

    def test_bash_c_grep_url_included(self, hook):
        """URL inside bash -c must not be dropped."""
        cmd = "bash -c \"grep 'https://169.254.169.254/latest/' f | xargs curl\""
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]

    def test_sed_command_substitution_url_included(self, hook):
        """URL inside $() in sed pattern must not be dropped."""
        cmd = 'sed "s/$(curl https://169.254.169.254/latest/meta-data/)/replacement/" file'
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]

    def test_grep_redirect_url_included(self, hook):
        """grep URL with output redirection must not be dropped."""
        cmd = "grep -o 'https://169.254.169.254/' file > /tmp/urls"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]

    def test_grep_pipe_to_python_url_included(self, hook):
        """grep URL piped to python must not be dropped."""
        cmd = "grep -o 'https://169.254.169.254/' file | python3 -c 'import urllib.request'"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]

    def test_curl_dollar_paren_grep_url_included(self, hook):
        """grep inside $() feeding curl — URL must not be dropped."""
        cmd = "curl $(grep -o 'https://169.254.169.254/latest/meta-data/' /some/file)"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]

    def test_curl_backtick_grep_url_included(self, hook):
        """grep inside backticks feeding curl — URL must not be dropped."""
        cmd = "curl `grep -o 'https://169.254.169.254/latest/meta-data/' /some/file`"
        urls = hook._extract_network_urls(cmd)
        assert len(urls) == 1
        assert "169.254.169.254" in urls[0]


# ---------------------------------------------------------------------------
# process_tool_call integration tests
# ---------------------------------------------------------------------------


class TestProcessToolCallSedPatterns:
    """Verify sed/grep/awk URL patterns are not blocked."""

    def test_sed_url_pattern_not_blocked(self, hook):
        """URL literals inside sed substitution patterns should not trigger SSRF."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "REPO=$(echo \"$URL\" | sed 's|https://github.com/||; s|/issues/.*||')"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is None, f"sed pattern should not be blocked, got: {result}"

    def test_sed_with_pipe_delimiter(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "sed 's|https://github.com/||' file.txt",
            },
        }
        assert hook.process_tool_call(tool_input) is None

    def test_sed_with_e_flag(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "sed -e 's|https://example.com/path||g' input.txt",
            },
        }
        assert hook.process_tool_call(tool_input) is None

    def test_grep_pattern_not_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "grep 'https://github.com/owner/repo' README.md",
            },
        }
        assert hook.process_tool_call(tool_input) is None

    def test_grep_with_flags_not_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "grep -rn 'https://example.com/' src/",
            },
        }
        assert hook.process_tool_call(tool_input) is None

    def test_awk_pattern_not_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "awk '/https://example.com/' access.log",
            },
        }
        assert hook.process_tool_call(tool_input) is None


class TestProcessToolCallSSRFStillBlocked:
    """Verify actual SSRF vectors are still caught."""

    def test_curl_to_metadata_still_blocked(self, hook):
        """Actual SSRF vectors must still be caught."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "curl http://169.254.169.254/latest/meta-data/"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "curl to metadata endpoint should be blocked"

    def test_curl_to_private_ip_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "curl http://192.168.1.1/admin"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None

    def test_wget_to_metadata_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "wget http://169.254.169.254/latest/meta-data/"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None

    def test_webfetch_blocked_scheme(self, hook):
        tool_input = {
            "tool_name": "WebFetch",
            "tool_input": {"url": "file:///etc/passwd"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "Blocked scheme" in result

    def test_webfetch_private_ip_blocked(self, hook):
        tool_input = {
            "tool_name": "WebFetch",
            "tool_input": {"url": "http://10.0.0.1/internal"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None

    def test_webfetch_metadata_blocked(self, hook):
        tool_input = {
            "tool_name": "WebFetch",
            "tool_input": {"url": "http://169.254.169.254/latest/meta-data/"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None

    def test_bash_file_scheme_still_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "curl file:///etc/shadow"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "Blocked scheme" in result

    def test_mixed_sed_and_curl_blocks_curl(self, hook):
        """sed URL is skipped but curl URL to private IP is still blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "echo \"$URL\" | sed 's|https://github.com/||' "
                    "&& curl http://169.254.169.254/latest/meta-data/"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_curl_dns_servers_still_blocked(self, hook):
        """curl --dns-servers=URL must not be bypassed by sed pattern detection."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "curl --dns-servers=http://169.254.169.254/latest/meta-data/",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "curl --dns-servers=metadata should be blocked"
        assert "169.254.169.254" in result

    def test_sed_cross_segment_injection_blocked(self, hook):
        """sed in one statement must not suppress SSRF in a later statement."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "echo sed 's|'; curl http://169.254.169.254/latest/meta-data/",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "cross-segment sed injection should be blocked"
        assert "169.254.169.254" in result

    def test_sed_cross_segment_and_injection_blocked(self, hook):
        """sed in one statement must not suppress SSRF after &&."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "echo sed 's/' && curl http://169.254.169.254/latest/meta-data/",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "cross-segment sed && injection should be blocked"
        assert "169.254.169.254" in result

    def test_grep_pipe_to_xargs_curl_blocked(self, hook):
        """grep -o URL piped to xargs curl must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": ("grep -oP 'http://169.254.169.254/latest/' file | xargs curl"),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "grep -o piped to xargs curl should be blocked"
        assert "169.254.169.254" in result

    def test_bash_c_nested_shell_blocked(self, hook):
        """bash -c with grep piped to curl must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": ("bash -c \"grep 'http://169.254.169.254/latest/' f | xargs curl\""),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "bash -c nested shell should be blocked"
        assert "169.254.169.254" in result

    def test_bash_x_c_nested_shell_blocked(self, hook):
        """bash -x -c nested shell must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "bash -x -c \"grep 'http://169.254.169.254/latest/' f | xargs curl\"",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "bash -x -c nested shell should be blocked"
        assert "169.254.169.254" in result

    def test_sed_command_substitution_blocked(self, hook):
        """sed with $() command substitution containing curl must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": ('sed "s/$(curl http://169.254.169.254/latest/meta-data/)/repl/" file'),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "sed $() command substitution should be blocked"
        assert "169.254.169.254" in result

    def test_sed_process_substitution_blocked(self, hook):
        """sed with <(curl URL) process substitution must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "sed 's@' <(curl http://169.254.169.254/latest/)"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "sed <() process substitution should be blocked"
        assert "169.254.169.254" in result

    def test_background_operator_then_curl_blocked(self, hook):
        """sed backgrounded with & followed by curl must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "sed 's@' f & curl http://169.254.169.254/latest/"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "& background separator should be blocked"
        assert "169.254.169.254" in result

    def test_awk_system_call_blocked(self, hook):
        """awk '/URL/ {system("curl "$0)}' must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "awk '/http://169.254.169.254/latest/ {system(\"curl \"$0)}' f",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "awk system() should be blocked"
        assert "169.254.169.254" in result

    def test_sed_write_flag_blocked(self, hook):
        """sed's w flag persists the URL with no shell redirection."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "sed -n 's|http://169.254.169.254/|&|w /tmp/u' file",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "sed w flag should be blocked"
        assert "169.254.169.254" in result

    def test_sed_execute_flag_blocked(self, hook):
        """sed's e flag executes the pattern space as a shell command."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "sed 's|http://169.254.169.254/||e' file"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "sed e flag should be blocked"
        assert "169.254.169.254" in result

    def test_sed_ampersand_replacement_piped_to_curl_blocked(self, hook):
        """sed 's,URL,&,' file | xargs curl must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "sed 's,http://169.254.169.254/latest/,&,' file | xargs curl",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "sed & replacement laundering should be blocked"
        assert "169.254.169.254" in result

    def test_brace_group_piped_to_curl_blocked(self, hook):
        """{ grep 'URL' file; } | xargs curl must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "{ grep 'http://169.254.169.254/latest/' file; } | xargs curl",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "compound grouping should be blocked"
        assert "169.254.169.254" in result

    def test_grep_piped_to_tee_then_curl_blocked(self, hook):
        """grep -o 'URL' f | tee /tmp/u; xargs curl < /tmp/u must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "grep -o 'http://169.254.169.254/latest/' file "
                    "| tee /tmp/u; xargs curl < /tmp/u"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "tee laundering should be blocked"
        assert "169.254.169.254" in result

    def test_grep_piped_to_bash_blocked(self, hook):
        """grep -o 'URL' file | bash must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "grep -o 'http://169.254.169.254/latest/' file | bash"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "grep piped to bash should be blocked"
        assert "169.254.169.254" in result

    def test_grep_redirect_then_curl_blocked(self, hook):
        """grep URL redirected to file then fed to curl must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "grep -o 'http://169.254.169.254/' file > /tmp/u && xargs curl < /tmp/u"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "grep redirect then curl should be blocked"
        assert "169.254.169.254" in result

    def test_grep_pipe_to_python_blocked(self, hook):
        """grep URL piped to python must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "grep -o 'http://169.254.169.254/' file | python3 -c 'import urllib.request'"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "grep piped to python should be blocked"
        assert "169.254.169.254" in result

    def test_eval_grep_blocked(self, hook):
        """eval with grep URL must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "eval \"grep 'http://169.254.169.254/' f\"",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "eval grep should be blocked"
        assert "169.254.169.254" in result

    def test_curl_dollar_paren_grep_blocked(self, hook):
        """curl $(grep -o 'URL' file) — command substitution bypass must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "curl $(grep -o 'http://169.254.169.254/latest/meta-data/' /some/file)"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "curl $() grep should be blocked"
        assert "169.254.169.254" in result

    def test_curl_backtick_grep_blocked(self, hook):
        """curl `grep -o 'URL' file` — backtick substitution bypass must be blocked."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": ("curl `grep -o 'http://169.254.169.254/latest/meta-data/' /some/file`"),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "curl backtick grep should be blocked"
        assert "169.254.169.254" in result


class TestProcessToolCallWebFetchUnchanged:
    """WebFetch tool calls bypass text-pattern detection entirely."""

    def test_webfetch_url_always_validated(self, hook):
        """WebFetch URLs are always network targets — no text-pattern bypass."""
        tool_input = {
            "tool_name": "WebFetch",
            "tool_input": {"url": "http://192.168.1.1/admin"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None


# ---------------------------------------------------------------------------
# Egress allowlist tests
# ---------------------------------------------------------------------------


class TestEgressAllowlistParsing:
    """Unit tests for _parse_egress_allowlist."""

    def test_empty_env(self, hook):
        with mock.patch.dict(os.environ, {}, clear=True):
            assert hook._parse_egress_allowlist() == set()

    def test_single_entry(self, hook):
        with mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "host.internal:443"}):
            result = hook._parse_egress_allowlist()
            assert ("host.internal", 443) in result

    def test_multiple_entries(self, hook):
        with mock.patch.dict(
            os.environ,
            {"FULLSEND_EGRESS_ALLOWLIST": "a.internal:443,b.internal:8443"},
        ):
            result = hook._parse_egress_allowlist()
            assert ("a.internal", 443) in result
            assert ("b.internal", 8443) in result

    def test_trailing_dot_stripped(self, hook):
        with mock.patch.dict(
            os.environ,
            {"FULLSEND_EGRESS_ALLOWLIST": "host.internal.:443"},
        ):
            result = hook._parse_egress_allowlist()
            assert ("host.internal", 443) in result

    def test_host_only_no_port(self, hook):
        with mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "host.internal"}):
            result = hook._parse_egress_allowlist()
            assert ("host.internal", 0) in result

    def test_whitespace_trimmed(self, hook):
        with mock.patch.dict(
            os.environ,
            {"FULLSEND_EGRESS_ALLOWLIST": " a.internal:443 , b.internal:8443 "},
        ):
            result = hook._parse_egress_allowlist()
            assert ("a.internal", 443) in result
            assert ("b.internal", 8443) in result

    def test_ipv6_brackets_stripped(self, hook):
        with mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "[::1]:443"}):
            result = hook._parse_egress_allowlist()
            assert ("::1", 443) in result
            assert ("[::1]", 443) not in result

    def test_ipv6_full_address_brackets_stripped(self, hook):
        with mock.patch.dict(
            os.environ,
            {"FULLSEND_EGRESS_ALLOWLIST": "[2001:db8::1]:8443"},
        ):
            result = hook._parse_egress_allowlist()
            assert ("2001:db8::1", 8443) in result

    def test_wildcard_entries_ignored(self, hook, capsys):
        with mock.patch.dict(
            os.environ,
            {"FULLSEND_EGRESS_ALLOWLIST": "*.internal:443,exact.host:8443"},
        ):
            result = hook._parse_egress_allowlist()
            assert len(result) == 1
            assert ("exact.host", 8443) in result
            captured = capsys.readouterr()
            assert "wildcard entry '*.internal:443'" in captured.err

    def test_malformed_port_warns(self, hook, capsys):
        with mock.patch.dict(
            os.environ,
            {"FULLSEND_EGRESS_ALLOWLIST": "host.internal:notaport,good.host:443"},
        ):
            result = hook._parse_egress_allowlist()
            assert len(result) == 1
            assert ("good.host", 443) in result
            captured = capsys.readouterr()
            assert "malformed port" in captured.err
            assert "host.internal:notaport" in captured.err


class TestEgressAllowlistValidateUrl:
    """Verify validate_url respects the egress allowlist on DNS failure."""

    def test_allowlisted_host_with_dns_failure_is_allowed(self, hook):
        """An allowlisted host should pass validation even when DNS fails."""
        url = "https://internal.host/api/v4/projects"
        with (
            mock.patch("socket.getaddrinfo", side_effect=socket.gaierror("no DNS")),
            mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "internal.host:443"}),
        ):
            result = hook.validate_url(url)
            assert result is None  # allowed

    def test_allowlisted_host_with_dns_timeout_is_allowed(self, hook):
        """An allowlisted host should pass validation even when DNS times out."""
        url = "https://internal.host/api/v4/projects"
        with (
            mock.patch("socket.getaddrinfo", side_effect=TimeoutError("timed out")),
            mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "internal.host:443"}),
        ):
            result = hook.validate_url(url)
            assert result is None  # allowed

    def test_allowlisted_host_still_blocks_dangerous_scheme(self, hook):
        """Allowlisting skips DNS only — scheme checks still apply."""
        url = "file://internal.host/etc/passwd"
        with mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "internal.host:443"}):
            result = hook.validate_url(url)
            assert result is not None  # blocked
            assert "Blocked scheme" in result

    def test_allowlisted_host_still_blocks_blocked_hostname(self, hook):
        """Allowlisting does not override the hostname blocklist."""
        url = "https://metadata.google.internal/something"
        with (
            mock.patch("socket.getaddrinfo", side_effect=socket.gaierror("no DNS")),
            mock.patch.dict(
                os.environ,
                {"FULLSEND_EGRESS_ALLOWLIST": "metadata.google.internal:443"},
            ),
        ):
            result = hook.validate_url(url)
            assert result is not None  # blocked
            assert "Blocked hostname" in result

    def test_non_allowlisted_host_dns_failure_still_blocks(self, hook):
        """Non-allowlisted hosts with DNS failure must still fail closed."""
        url = "https://evil.internal.corp/steal"
        with mock.patch("socket.getaddrinfo", side_effect=socket.gaierror("no DNS")):
            result = hook.validate_url(url)
            assert result is not None
            assert "fail-closed" in result

    def test_allowlisted_host_wrong_port_still_blocks(self, hook):
        """Allowlist entry for port 443 does not apply to port 8080."""
        url = "https://internal.host:8080/api"
        with (
            mock.patch("socket.getaddrinfo", side_effect=socket.gaierror("no DNS")),
            mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "internal.host:443"}),
        ):
            result = hook.validate_url(url)
            assert result is not None
            assert "fail-closed" in result

    def test_allowlisted_host_wildcard_port(self, hook):
        """Allowlist entry without port acts as wildcard for any port."""
        url = "https://internal.host:8080/api"
        with (
            mock.patch("socket.getaddrinfo", side_effect=socket.gaierror("no DNS")),
            mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "internal.host"}),
        ):
            result = hook.validate_url(url)
            assert result is None  # allowed

    def test_allowlisted_host_dns_resolves_to_private_still_blocks(self, hook):
        """When DNS succeeds, the rebinding check still applies for allowlisted hosts."""
        url = "https://internal.host/api"
        with (
            mock.patch(
                "socket.getaddrinfo",
                return_value=[(socket.AF_INET, 1, 0, "", ("10.0.0.1", 0))],
            ),
            mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "internal.host:443"}),
        ):
            result = hook.validate_url(url)
            assert result is not None
            assert "rebinding" in result.lower() or "blocked" in result.lower()

    def test_allowlisted_http_default_port(self, hook):
        """HTTP URL with no explicit port matches allowlist entry on port 80."""
        url = "http://internal.host/api"
        with (
            mock.patch("socket.getaddrinfo", side_effect=socket.gaierror("no DNS")),
            mock.patch.dict(os.environ, {"FULLSEND_EGRESS_ALLOWLIST": "internal.host:80"}),
        ):
            result = hook.validate_url(url)
            assert result is None  # allowed

    def test_no_allowlist_dns_failure_still_blocks(self, hook):
        """Without FULLSEND_EGRESS_ALLOWLIST, DNS failure always blocks."""
        url = "https://internal.host/api"
        with mock.patch("socket.getaddrinfo", side_effect=socket.gaierror("no DNS")):
            # Ensure env var is not set.
            env = dict(os.environ)
            env.pop("FULLSEND_EGRESS_ALLOWLIST", None)
            with mock.patch.dict(os.environ, env, clear=True):
                result = hook.validate_url(url)
                assert result is not None
                assert "fail-closed" in result

    def test_ipv6_bracket_allowlist_entry_matches(self, hook):
        """IPv6 allowlist entry with brackets matches urlparse().hostname (no brackets)."""
        # urlparse("https://[::1]:443/").hostname returns "::1" (no brackets),
        # so a bracket-notation allowlist entry must strip brackets to match.
        url = "https://internal.host/api"
        with (
            mock.patch("socket.getaddrinfo", side_effect=socket.gaierror("no DNS")),
            mock.patch.dict(
                os.environ,
                {"FULLSEND_EGRESS_ALLOWLIST": "[internal.host]:443"},
            ),
        ):
            result = hook.validate_url(url)
            assert result is None  # allowed


# ===========================================================================
# Inert-pipeline exemption tests (issue #6541)
#
# The _pipeline_is_inert() path skips SSRF URL validation when every stage of
# a Bash command is a known non-network command.  Security regression tests
# verify that network-capable commands, shell reentry, command substitution,
# process substitution, and /dev/tcp devices all defeat the exemption.
# ===========================================================================


class TestHasCommandSubstitution:
    """Unit tests for command/process substitution detection."""

    def test_dollar_paren(self, hook):
        assert hook._has_command_substitution("echo $(whoami)")

    def test_backtick(self, hook):
        assert hook._has_command_substitution("echo `whoami`")

    def test_process_substitution_lt(self, hook):
        assert hook._has_command_substitution("diff <(ls) <(ls)")

    def test_process_substitution_gt(self, hook):
        assert hook._has_command_substitution("tee >(cat)")

    def test_dollar_paren_in_single_quotes_is_literal(self, hook):
        assert not hook._has_command_substitution("echo '$(not_a_subshell)'")

    def test_backtick_in_single_quotes_is_literal(self, hook):
        assert not hook._has_command_substitution("echo '`not_a_subshell`'")

    def test_no_substitution(self, hook):
        assert not hook._has_command_substitution("echo hello world")

    def test_dollar_sign_without_paren(self, hook):
        assert not hook._has_command_substitution("echo $HOME")

    def test_escaped_dollar_paren(self, hook):
        assert not hook._has_command_substitution("echo \\$(whoami)")

    def test_single_quote_inside_double_quotes_does_not_hide_substitution(self, hook):
        """Single quote inside double quotes is literal — must not enter sq mode."""
        assert hook._has_command_substitution("""echo "'"$(curl http://example.com)"'" """)

    def test_backtick_inside_double_quotes_is_active(self, hook):
        assert hook._has_command_substitution('echo "`whoami`"')

    def test_dollar_paren_inside_double_quotes_is_active(self, hook):
        assert hook._has_command_substitution('echo "$(whoami)"')

    def test_process_substitution_inside_double_quotes_is_literal(self, hook):
        """<() inside double quotes is literal text — not process substitution."""
        assert not hook._has_command_substitution('echo "<(not_a_subshell)"')

    def test_gt_paren_inside_double_quotes_is_literal(self, hook):
        """>() inside double quotes is literal text — not process substitution."""
        assert not hook._has_command_substitution('echo ">(not_a_subshell)"')


# ---------------------------------------------------------------------------
# _split_command_stages tests
# ---------------------------------------------------------------------------


class TestSplitCommandStages:
    """Unit tests for quote-aware pipeline/sequence splitting."""

    def test_simple_pipe(self, hook):
        assert hook._split_command_stages("echo hello | grep hello") == [
            "echo hello",
            "grep hello",
        ]

    def test_semicolon(self, hook):
        assert hook._split_command_stages("echo a; echo b") == ["echo a", "echo b"]

    def test_and_operator(self, hook):
        assert hook._split_command_stages("echo a && echo b") == ["echo a", "echo b"]

    def test_or_operator(self, hook):
        assert hook._split_command_stages("echo a || echo b") == ["echo a", "echo b"]

    def test_pipe_inside_single_quotes_not_split(self, hook):
        result = hook._split_command_stages("sed 's|https://github.com/||'")
        assert len(result) == 1
        assert "sed" in result[0]

    def test_pipe_inside_double_quotes_not_split(self, hook):
        result = hook._split_command_stages('echo "a|b"')
        assert len(result) == 1

    def test_mixed_pipe_and_quoted_pipe(self, hook):
        result = hook._split_command_stages(
            "echo \"https://github.com/o/r/issues/925\" | sed 's|https://github.com/||'"
        )
        assert len(result) == 2
        assert "echo" in result[0]
        assert "sed" in result[1]

    def test_newline_splits(self, hook):
        result = hook._split_command_stages("echo a\necho b")
        assert len(result) == 2


# ---------------------------------------------------------------------------
# _extract_base_command tests
# ---------------------------------------------------------------------------


class TestExtractBaseCommand:
    """Unit tests for command name extraction from a stage."""

    def test_simple_command(self, hook):
        assert hook._extract_base_command("echo hello") == "echo"

    def test_command_with_path_is_unknown(self, hook):
        """A path-qualified executable is not trusted by basename (/tmp/echo)."""
        assert hook._extract_base_command("/usr/bin/sed 's|x||'") is None
        assert hook._extract_base_command("/tmp/echo hello") is None

    def test_variable_assignment_then_command_is_unknown(self, hook):
        """An assignment prefix can load code before the command runs."""
        assert hook._extract_base_command("FOO=bar echo hello") is None
        assert hook._extract_base_command("LD_PRELOAD=/tmp/x.so grep x f") is None

    def test_variable_assignment_only(self, hook):
        # A bare assignment is still parsed — the "command" is the value part.
        # _pipeline_is_inert handles this by checking the result against
        # _INERT_COMMANDS, which won't match the value → fail closed.
        result = hook._extract_base_command("FOO=bar")
        # "FOO=bar" splits into one token which matches the assignment pattern,
        # so there's no command left → None
        assert result is None

    def test_redirection_skipped(self, hook):
        assert hook._extract_base_command("echo hello > /tmp/out") == "echo"

    def test_here_string_skipped(self, hook):
        assert hook._extract_base_command("sed 's|x||' <<< input") == "sed"

    def test_quoted_command(self, hook):
        assert hook._extract_base_command("'echo' hello") == "echo"

    def test_trailing_slash_returns_none(self, hook):
        """A path ending in / yields an empty basename — return None, not ''."""
        assert hook._extract_base_command("/usr/bin/") is None


# ---------------------------------------------------------------------------
# _pipeline_is_inert tests
# ---------------------------------------------------------------------------


class TestPipelineIsInert:
    """Unit tests for the inert-pipeline detector."""

    # --- Inert commands (should return True) ---

    def test_echo_pipe_cut(self, hook):
        cmd = "echo https://github.com/o/r/issues/925 | cut -d/ -f4,5"
        assert hook._pipeline_is_inert(cmd)

    def test_echo_alone(self, hook):
        assert hook._pipeline_is_inert("echo https://github.com/foo")

    def test_cat_pipe_grep_pipe_sort(self, hook):
        cmd = "cat file.txt | grep 'https://example.com' | sort"
        assert hook._pipeline_is_inert(cmd)

    def test_printf_pipe_head(self, hook):
        assert hook._pipeline_is_inert("printf '%s\\n' https://example.com | head -1")

    def test_echo_pipe_jq(self, hook):
        assert hook._pipeline_is_inert('echo \'{"url":"https://example.com"}\' | jq .url')

    def test_echo_pipe_tr(self, hook):
        assert hook._pipeline_is_inert("echo https://example.com | tr '/' ' '")

    def test_echo_pipe_wc(self, hook):
        assert hook._pipeline_is_inert("echo https://example.com | wc -c")

    def test_echo_semicolon_echo(self, hook):
        assert hook._pipeline_is_inert("echo https://github.com/a; echo https://github.com/b")

    # --- Non-inert commands (should return False) ---

    def test_curl_not_inert(self, hook):
        assert not hook._pipeline_is_inert("curl https://evil.com")

    def test_wget_not_inert(self, hook):
        assert not hook._pipeline_is_inert("wget https://evil.com")

    def test_python_not_inert(self, hook):
        assert not hook._pipeline_is_inert("python3 -c 'import urllib'")

    def test_xargs_not_inert(self, hook):
        assert not hook._pipeline_is_inert("echo https://evil.com | xargs curl")

    def test_pipe_to_curl_not_inert(self, hook):
        assert not hook._pipeline_is_inert("echo https://evil.com | curl -K -")

    def test_unknown_command_not_inert(self, hook):
        assert not hook._pipeline_is_inert("myprogram https://example.com")

    def test_sed_not_inert(self, hook):
        """sed has GNU 'e' command/flag for shell execution — not safe."""
        assert not hook._pipeline_is_inert(
            "echo https://github.com/o/r/issues/925 | sed 's|https://github.com/||; s|/issues/.*||'"
        )

    def test_sed_alone_not_inert(self, hook):
        """sed with here-string — not safe due to GNU 'e' command."""
        assert not hook._pipeline_is_inert(
            "sed 's|https://github.com/||'  <<< \"https://github.com/o/r/issues/925\""
        )

    def test_awk_not_inert(self, hook):
        """awk has system(), getline-from-pipe — not safe."""
        assert not hook._pipeline_is_inert("echo 'hello' | awk '{print $1}'")

    def test_gawk_not_inert(self, hook):
        """gawk has native TCP/UDP networking via /inet/ files."""
        assert not hook._pipeline_is_inert("echo data | gawk '{print}'")

    def test_mawk_not_inert(self, hook):
        assert not hook._pipeline_is_inert("echo data | mawk '{print}'")

    def test_nawk_not_inert(self, hook):
        assert not hook._pipeline_is_inert("echo data | nawk '{print}'")

    def test_tee_not_inert(self, hook):
        """tee writes to arbitrary files — not safe for inert exemption."""
        assert not hook._pipeline_is_inert("echo data | tee /tmp/out")

    # --- Shell reentry (should return False) ---

    def test_bash_c_not_inert(self, hook):
        assert not hook._pipeline_is_inert('bash -c "echo https://evil.com"')

    def test_sh_c_not_inert(self, hook):
        assert not hook._pipeline_is_inert('sh -c "echo https://evil.com"')

    def test_eval_not_inert(self, hook):
        assert not hook._pipeline_is_inert('eval "echo https://evil.com"')

    def test_exec_not_inert(self, hook):
        assert not hook._pipeline_is_inert("exec curl https://evil.com")

    def test_bash_lc_combined_flag_not_inert(self, hook):
        """bash -lc combines login + command flags — still shell reentry."""
        assert not hook._pipeline_is_inert('bash -lc "curl https://evil.com"')

    def test_sh_xc_combined_flag_not_inert(self, hook):
        """sh -xc combines trace + command flags — still shell reentry."""
        assert not hook._pipeline_is_inert('sh -xc "curl https://evil.com"')

    def test_bash_norc_c_flag_not_inert(self, hook):
        """bash --norc -c has intervening long flag — still shell reentry."""
        assert not hook._pipeline_is_inert('bash --norc -c "curl https://evil.com"')

    def test_bash_ic_combined_flag_not_inert(self, hook):
        """bash -ic combines interactive + command — still shell reentry."""
        assert not hook._pipeline_is_inert('bash -ic "curl https://evil.com"')

    # --- Command substitution (should return False) ---

    def test_dollar_paren_not_inert(self, hook):
        assert not hook._pipeline_is_inert(
            "REPO=$(echo \"$ISSUE_URL\" | sed 's|https://github.com/||')"
        )

    def test_backtick_not_inert(self, hook):
        assert not hook._pipeline_is_inert("REPO=`echo https://example.com | cut -d/ -f3`")

    def test_process_substitution_not_inert(self, hook):
        assert not hook._pipeline_is_inert("diff <(curl https://evil.com) <(echo foo)")

    # --- Double-quote context (should return False) ---

    def test_single_quote_in_double_quotes_hides_subst_not_inert(self, hook):
        """Single quote inside double quotes must not hide command substitution."""
        cmd = """echo "'"$(curl http://metadata-endpoint/latest/)"'" """
        assert not hook._pipeline_is_inert(cmd)

    # --- Literal substitution in single quotes IS inert ---

    def test_literal_dollar_paren_in_single_quotes_is_inert(self, hook):
        cmd = "echo '$(not_a_subshell)' | grep pattern"
        assert hook._pipeline_is_inert(cmd)


# ---------------------------------------------------------------------------
# process_tool_call integration tests — false positives fixed (#6541)
# ---------------------------------------------------------------------------


class TestProcessToolCallInertPipeline:
    """Verify URL literals in inert pipelines are not blocked (issue #6541).

    Note: sed and awk pipelines are no longer exempted (removed from
    _INERT_COMMANDS due to code-execution capabilities).  Those false
    positives are addressed by PR #6536's per-argument context approach.
    """

    def test_echo_url_piped_to_cut_not_blocked(self, hook):
        """Reproduction case from #6541: echo URL | cut."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "echo https://github.com/o/r/issues/925 | cut -d/ -f4,5",
            },
        }
        assert hook.process_tool_call(tool_input) is None

    def test_echo_url_alone_not_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "echo https://github.com/fullsend-ai/agents/issues/925",
            },
        }
        assert hook.process_tool_call(tool_input) is None

    def test_cat_grep_url_not_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "cat README.md | grep 'https://github.com/owner/repo'",
            },
        }
        assert hook.process_tool_call(tool_input) is None

    def test_echo_url_pipe_jq_not_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": 'echo \'{"url":"https://github.com/o/r"}\' | jq .url',
            },
        }
        assert hook.process_tool_call(tool_input) is None

    def test_printf_url_pipe_head_not_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "printf '%s\\n' https://github.com/o/r | head -1",
            },
        }
        assert hook.process_tool_call(tool_input) is None

    # ---------------------------------------------------------------------------
    # process_tool_call integration tests — SSRF vectors still blocked
    # ---------------------------------------------------------------------------

    def test_dev_tcp_as_string_data_allowed(self, hook):
        """A /dev/tcp mention in echo text is data, not a redirection (no URL, no block)."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": 'echo "/dev/tcp/host/80 is a bash device"'},
        }
        assert hook.process_tool_call(tool_input) is None


class TestProcessToolCallInertSSRFStillBlocked:
    """Verify actual SSRF vectors are still caught after the inert-pipeline change."""

    def test_curl_to_metadata_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "curl http://169.254.169.254/latest/meta-data/"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None, "curl to metadata endpoint should be blocked"
        assert "169.254.169.254" in result

    def test_curl_to_private_ip_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "curl http://192.168.1.1/admin"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None

    def test_wget_to_metadata_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "wget http://169.254.169.254/latest/meta-data/"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None

    def test_webfetch_blocked_scheme(self, hook):
        tool_input = {
            "tool_name": "WebFetch",
            "tool_input": {"url": "file:///etc/passwd"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "Blocked scheme" in result

    def test_webfetch_private_ip_blocked(self, hook):
        tool_input = {
            "tool_name": "WebFetch",
            "tool_input": {"url": "http://10.0.0.1/internal"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None

    def test_webfetch_metadata_blocked(self, hook):
        tool_input = {
            "tool_name": "WebFetch",
            "tool_input": {"url": "http://169.254.169.254/latest/meta-data/"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None

    def test_bash_file_scheme_still_blocked(self, hook):
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "curl file:///etc/shadow"},
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "Blocked scheme" in result

    def test_echo_then_curl_both_urls_validated(self, hook):
        """echo URL && curl URL — curl makes the pipeline non-inert."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "echo https://example.test/safe"
                    " && curl http://169.254.169.254/latest/meta-data/"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "SSRF blocked" in result

    def test_grep_pipe_to_xargs_curl_blocked(self, hook):
        """grep URL piped to xargs curl must be blocked (xargs not inert)."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "grep -oP 'http://169.254.169.254/latest/' file | xargs curl",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_bash_c_nested_shell_blocked(self, hook):
        """bash -c with grep piped to curl must be blocked (shell reentry)."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": ("bash -c \"grep 'http://169.254.169.254/latest/' f | xargs curl\""),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_eval_blocked(self, hook):
        """eval re-parses the string — URL must not be exempted."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": 'eval "curl http://169.254.169.254/latest/meta-data/"',
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_command_substitution_with_curl_blocked(self, hook):
        """$() with curl inside — substitution defeats inert check."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "RESULT=$(curl http://169.254.169.254/latest/meta-data/)",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_process_substitution_blocked(self, hook):
        """<(curl URL) process substitution — must not be exempted."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "diff <(curl http://169.254.169.254/latest/) /dev/null",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_unknown_command_still_validated(self, hook):
        """Unknown commands must not be exempted — fail closed."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "myprogram http://169.254.169.254/latest/meta-data/",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_backgrounded_then_curl_blocked(self, hook):
        """echo URL & curl URL — background operator creates two stages."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "echo https://example.test/safe & curl http://169.254.169.254/latest/meta-data/"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "SSRF blocked" in result

    def test_sed_cross_segment_semicolon_blocked(self, hook):
        """sed in one statement must not exempt curl in a later statement."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": ("echo sed 's|'; curl http://169.254.169.254/latest/meta-data/"),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_sed_cross_segment_and_blocked(self, hook):
        """sed in one statement must not exempt curl after &&."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": ("echo sed 's/' && curl http://169.254.169.254/latest/meta-data/"),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_awk_system_bypass_blocked(self, hook):
        """awk system() can execute arbitrary commands — not inert."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": (
                    "awk 'BEGIN{system(\"curl http://169.254.169.254/latest/meta-data/\")}'"
                ),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_sed_e_command_bypass_blocked(self, hook):
        """GNU sed 'e' flag can execute shell — sed pipelines are not inert.

        Uses a URL visible to URL_PATTERN to confirm the pipeline goes
        through full validation now that sed is removed from _INERT_COMMANDS.
        """
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "echo http://169.254.169.254/latest/meta-data/ | sed 's/^/GET /e'",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_dev_tcp_redirect_not_inert(self, hook):
        """Bash /dev/tcp makes network connections via redirections."""
        assert not hook._pipeline_is_inert("echo 'GET / HTTP/1.0' > /dev/tcp/169.254.169.254/80")

    def test_dev_udp_redirect_not_inert(self, hook):
        """Bash /dev/udp makes network connections via redirections."""
        assert not hook._pipeline_is_inert("read -r line < /dev/udp/10.0.0.1/53")

    def test_dev_tcp_in_otherwise_inert_pipeline_not_inert(self, hook):
        """echo + /dev/tcp — both are 'inert' commands but /dev/tcp is a network device."""
        assert not hook._pipeline_is_inert("echo 'GET /' > /dev/tcp/metadata.google.internal/80")

    def test_dev_tcp_single_quoted_evasion_not_inert(self, hook):
        """Shell quotes in /dev/tcp path: /dev/'tcp'/HOST/80 is equivalent at runtime."""
        assert not hook._pipeline_is_inert("read line < /dev/'tcp'/169.254.169.254/80")

    def test_dev_tcp_double_quoted_evasion_not_inert(self, hook):
        """Shell quotes in /dev/tcp path: /dev/\"tcp\"/HOST/80 is equivalent at runtime."""
        assert not hook._pipeline_is_inert('echo "GET /" > /dev/"tcp"/169.254.169.254/80')

    def test_dev_udp_single_quoted_evasion_not_inert(self, hook):
        """Shell quotes in /dev/udp path: /dev/'udp'/HOST/53 is equivalent at runtime."""
        assert not hook._pipeline_is_inert("read line < /dev/'udp'/10.0.0.1/53")

    def test_dev_tcp_partial_quote_evasion_not_inert(self, hook):
        """Partial quoting: /dev/tc'p'/HOST/80 resolves to /dev/tcp/HOST/80."""
        assert not hook._pipeline_is_inert("read line < /dev/tc'p'/169.254.169.254/80")

    def test_dev_tcp_variable_expansion_not_inert(self, hook):
        """Variable expansion: /dev/$PROTO/HOST/80 could resolve to /dev/tcp/."""
        assert not hook._pipeline_is_inert(
            "export PROTO=tcp; read line < /dev/$PROTO/169.254.169.254/80"
        )

    def test_dev_tcp_variable_expansion_curly_not_inert(self, hook):
        """Variable expansion with braces: /dev/${PROTO}/HOST/80."""
        assert not hook._pipeline_is_inert("read line < /dev/${PROTO}/169.254.169.254/80")

    @pytest.mark.parametrize(
        "command",
        [
            "/tmp/echo http://169.254.169.254/latest/meta-data",
            "LD_PRELOAD=/tmp/x.so grep x http://169.254.169.254/ f",
            "echo http://169.254.169.254/ | sort --compress-program=/tmp/fetch",
            "echo http://169.254.169.254/ | rg --p\\re /tmp/fetch x",
        ],
    )
    def test_untrusted_executable_or_exec_flag_still_validated(self, hook, command):
        """Path-qualified commands, assignment prefixes and helper-exec flags fail closed."""
        result = hook.process_tool_call({"tool_name": "Bash", "tool_input": {"command": command}})
        assert result is not None
        assert "169.254.169.254" in result

    def test_tee_write_not_exempted(self, hook):
        """tee writes to arbitrary files — no longer exempted as inert."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": "echo http://169.254.169.254/latest/meta-data/ | tee /tmp/out",
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result

    def test_single_quote_in_dq_hides_substitution_blocked(self, hook):
        """Single quote inside double quotes must not hide command substitution."""
        tool_input = {
            "tool_name": "Bash",
            "tool_input": {
                "command": ("""echo "'"$(curl http://169.254.169.254/latest/meta-data/)"'" """),
            },
        }
        result = hook.process_tool_call(tool_input)
        assert result is not None
        assert "169.254.169.254" in result


# ---------------------------------------------------------------------------
# Subprocess-based integration tests — stdin/stdout/exit-code protocol
# ---------------------------------------------------------------------------


def _run_hook(stdin_data: str) -> tuple[int, str]:
    """Run the hook as a subprocess, matching the convention in other hook tests."""
    result = subprocess.run(
        [sys.executable, HOOK_PATH],
        input=stdin_data,
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout


class TestSubprocessProtocol:
    """Verify the hook's stdin/stdout/exit-code contract via subprocess."""

    def test_inert_pipeline_allowed(self):
        """Inert pipeline should exit 0 with no stdout (allowed)."""
        payload = json.dumps(
            {
                "tool_name": "Bash",
                "tool_input": {
                    "command": "echo https://github.com/o/r/issues/925 | cut -d/ -f4,5",
                },
            }
        )
        code, stdout = _run_hook(payload)
        assert code == 0
        assert stdout == ""

    def test_curl_to_metadata_blocked(self):
        """curl to metadata endpoint should exit 1 with block JSON on stdout."""
        payload = json.dumps(
            {
                "tool_name": "Bash",
                "tool_input": {
                    "command": "curl http://169.254.169.254/latest/meta-data/",
                },
            }
        )
        code, stdout = _run_hook(payload)
        assert code == 1
        response = json.loads(stdout)
        assert response["decision"] == "block"
        assert "169.254.169.254" in response["reason"]

    def test_empty_input_allowed(self):
        """Empty stdin should exit 0 (no tool call to validate)."""
        code, stdout = _run_hook("")
        assert code == 0

    def test_malformed_json_blocked(self):
        """Unparseable JSON should exit 1 (fail-closed)."""
        code, stdout = _run_hook("{not json")
        assert code == 1
        response = json.loads(stdout)
        assert response["decision"] == "block"
