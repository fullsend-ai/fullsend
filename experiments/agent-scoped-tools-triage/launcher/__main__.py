"""CLI entry point: python -m launcher --repo org/repo --issue 42"""

import argparse
from pathlib import Path

from .auth import get_token_from_gh_cli, get_token_from_github_app
from .orchestrator import launch_agent


def main() -> None:
    parser = argparse.ArgumentParser(description="Launch triage agent")

    # Token source (mutually exclusive)
    token_group = parser.add_mutually_exclusive_group()
    token_group.add_argument("--token", help="GitHub token (for testing)")
    token_group.add_argument("--pem", help="Path to GitHub App PEM key (for production)")

    # GitHub App auth options (only needed with --pem)
    parser.add_argument("--client-id", help="GitHub App Client ID")
    parser.add_argument("--installation-id", type=int, help="GitHub App Installation ID")
    parser.add_argument("--repo-id", type=int, help="Repository ID (for scoped token)")

    # Required
    parser.add_argument("--repo", required=True, help="Repository in org/repo format")
    parser.add_argument("--issue", required=True, type=int, help="Issue number to triage")

    args = parser.parse_args()

    # The working dir is the experiment root (parent of launcher/)
    base_dir = Path(__file__).parent.parent

    # Get token
    if args.token:
        print("Using provided token...")
        token = args.token
    elif args.pem:
        if not args.client_id or not args.installation_id:
            parser.error("--pem requires --client-id and --installation-id")
        print("Authenticating as GitHub App...")
        token = get_token_from_github_app(
            args.pem,
            args.client_id,
            args.installation_id,
            args.repo_id,
        )
    else:
        print("Getting token from gh CLI...")
        token = get_token_from_gh_cli()

    launch_agent(token, args.repo, args.issue, base_dir)


if __name__ == "__main__":
    main()
