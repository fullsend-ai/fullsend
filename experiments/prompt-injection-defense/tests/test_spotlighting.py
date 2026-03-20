# tests/test_spotlighting.py
from unittest.mock import MagicMock, patch

from defenses.spotlighting import run_spotlighting


@patch("defenses.spotlighting.get_client")
def test_clean_commit_not_detected(mock_get_client):
    mock_client = MagicMock()
    mock_response = MagicMock()
    mock_response.content = [
        MagicMock(text='{"assessment": "clean", "reasoning": "Normal commit"}')
    ]
    mock_client.messages.create.return_value = mock_response
    mock_get_client.return_value = mock_client

    result = run_spotlighting("fix: update readme", injection_goal="none")
    assert result.detected is False


@patch("defenses.spotlighting.get_client")
def test_prompt_includes_data_markers(mock_get_client):
    mock_client = MagicMock()
    mock_response = MagicMock()
    mock_response.content = [MagicMock(text='{"assessment": "clean", "reasoning": "ok"}')]
    mock_client.messages.create.return_value = mock_response
    mock_get_client.return_value = mock_client

    run_spotlighting("fix: something", injection_goal="none")

    call_args = mock_client.messages.create.call_args
    user_content = call_args.kwargs["messages"][0]["content"]
    assert "<<<UNTRUSTED_DATA>>>" in user_content
    assert "<<<END_UNTRUSTED_DATA>>>" in user_content
    assert "fix: something" in user_content
