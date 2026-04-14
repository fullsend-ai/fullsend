# Experiment 005 Results

_Placeholder — run `uv run python runner.py` to populate this file._

Run the experiment with:

```bash
cd experiments/reasoning-monitor
uv run python runner.py --dry-run           # test without LLM calls
uv run python runner.py --monitor canary    # canary hook only
uv run python runner.py --monitor allowlist # allowlist hook only
uv run python runner.py --model haiku       # full run with Haiku
uv run python runner.py --model sonnet      # full run with Sonnet
```
