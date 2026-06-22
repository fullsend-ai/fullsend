# Getting Started

This section contains easy and to the point guides to help you
set up Fullsend. These are intended to be read in a certain order:

1. **Mint enrollment** — before configuring anything, your org or repo
   must be enrolled in a fullsend token mint service so the mint
   accepts token requests from your GitHub Actions workflows.
   The CLI defaults to the hosted mint. To enroll, contact the
   fullsend team in the internal Slack channel with your GitHub org
   name (for org mode) or `owner/repo` (for per-repo mode). To deploy
   and manage your own mint instead, see the
   [Mint administration](../infrastructure/mint-administration.md) guide.

   > **Note:** Self-service enrollment is not yet available.
   > [Public mint mode](https://github.com/fullsend-ai/fullsend/pull/1580)
   > will remove the need for per-org/repo enrollment, but is still
   > in progress.

2. [Getting Inference](getting-inference.md)
3. [Configuring GitHub](configuring-github.md)
4. [Organization Mode](org-mode.md)
