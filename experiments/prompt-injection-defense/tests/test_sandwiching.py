# tests/test_sandwiching.py
from unittest.mock import MagicMock, patch

from defenses.sandwiching import run_sandwiching


@patch("defenses.sandwiching.get_client")
def test_clean_commit_not_detected(mock_get_client):
    mock_client = MagicMock()
    mock_response = MagicMock()
    mock_response.content = [
        MagicMock(text='{"assessment": "clean", "reasoning": "Normal commit"}')
    ]
    mock_client.messages.create.return_value = mock_response
    mock_get_client.return_value = mock_client

    result = run_sandwiching("fix: update readme", injection_goal="none")
    assert result.detected is False


@patch("defenses.sandwiching.get_client")
def test_prompt_repeats_instruction_after_input(mock_get_client):
    mock_client = MagicMock()
    mock_response = MagicMock()
    mock_response.content = [MagicMock(text='{"assessment": "clean", "reasoning": "ok"}')]
    mock_client.messages.create.return_value = mock_response
    mock_get_client.return_value = mock_client

    run_sandwiching("fix: something", injection_goal="none")

    call_args = mock_client.messages.create.call_args
    user_content = call_args.kwargs["messages"][0]["content"]
    commit_pos = user_content.index("fix: something")
    reminder_pos = user_content.index("Remember:")
    assert commit_pos < reminder_pos
