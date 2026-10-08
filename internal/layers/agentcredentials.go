package layers

// AgentCredentials holds agent identity (role, name, slug) and app credentials for layer operations.
type AgentCredentials struct {
	Role     string
	Name     string
	Slug     string
	PEM      string
	ClientID string
	AppID    int
}
