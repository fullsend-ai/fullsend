import os

import anthropic

MODEL = "claude-sonnet-4-6"
TEMPERATURE = 0


def get_client() -> anthropic.AnthropicVertex:
    return anthropic.AnthropicVertex(
        project_id=os.environ["ANTHROPIC_VERTEX_PROJECT_ID"],
        region=os.environ.get("CLOUD_ML_REGION", "us-east5"),
    )
