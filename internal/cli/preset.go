package cli

// stubConfigYAML is the stub overlay committed as .fullsend/config.yaml
// when a preset base is provided via --config. It contains comments
// explaining the layered relationship and minimal empty override fields
// for the adopter to customize.
const stubConfigYAML = `# fullsend per-repo configuration (overlay)
# https://github.com/fullsend-ai/fullsend
#
# This file is the per-repo overlay for fullsend configuration.
# Base settings are provided by config.base.yaml (vendor preset).
# Values set here override the base layer. Omitted fields inherit
# from config.base.yaml, then from compiled-in code defaults.
#
# See ADR 0069 for the layered configuration model.

# Uncomment and customize fields as needed:
# roles: []
# runtime: ""
# agents: []
# kill_switch: false
`
