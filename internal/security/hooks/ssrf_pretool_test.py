"""Tests for ssrf_pretool.py PreToolUse hook."""

from __future__ import annotations

import importlib.util
import os

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
