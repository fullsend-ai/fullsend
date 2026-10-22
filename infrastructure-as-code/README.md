# fullsend-ai Infrastructure

Infrastructure as Code (IaC) for fullsend-ai managed with [OpenTofu](https://opentofu.org/).

## Architecture & Conventions

Infrastructure is organized with separation of concerns:

- **Isolated State per Component**: Each directory manages its own independent state file and provider configurations, preventing cross-domain blast radius.
- **Provider-Scoped Variables**: Variables are grouped into platform-specific objects (e.g., `cloudflare = { ... }`).
- **Path-Based CI/CD**: Each component has a dedicated GitHub Actions workflow in the repo-root `.github/workflows/` triggered only when its files change.

## Directory Layout

```text
infrastructure-as-code/
├── cloudflare/            # Cloudflare DNS, edge settings, and SSL
│   ├── main.tf
│   ├── variables.tf
│   ├── versions.tf
│   ├── terraform.tfvars.example
│   └── README.md
└── README.md
```

## Components

| Component | Description | Docs |
|---|---|---|
| [Cloudflare](./cloudflare/) | Edge settings, DNS, and SSL configurations | [cloudflare/README.md](./cloudflare/README.md) |

## Adding a New Component

1. Create a new directory for the provider or platform (e.g., `aws/`, `kubernetes/`).
2. Include standard OpenTofu configuration files (`versions.tf`, `variables.tf`, `main.tf`, `terraform.tfvars.example`, `README.md`).
3. Add a dedicated workflow in the repo-root `.github/workflows/<component>.yml` with `paths:` filtering to `infrastructure-as-code/<component>/**` (follow [CI Workflows](../docs/contributing/ci-workflows.md) conventions).
4. Document the new component in this table.

## Tooling

- [OpenTofu](https://opentofu.org/) >= 1.5.0
