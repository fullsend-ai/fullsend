package authorization

// Gate defines a label-gated elevated permission. Human collaborators apply
// the AllowedLabel after the NeededLabel signals that elevation may be required.
type Gate struct {
	Name          string
	NeededLabel   string
	AllowedLabel  string
	FilePatterns  []string
	MintElevation map[string]string
}
