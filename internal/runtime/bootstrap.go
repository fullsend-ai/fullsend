package runtime

// BootstrapInput is the portable contract every runtime needs to provision
// agent content into the sandbox. Implementations live outside this package
// (runner adapter, tests).
type BootstrapInput interface {
	SandboxName() string
	AgentPath() string
	AgentName() string
	SkillDirs() []string
	PluginDirs() []string
}
