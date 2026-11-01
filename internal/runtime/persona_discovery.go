package runtime

// RegisteredPersona is one sub-agents/*.md file that pi and codex
// discovery registers (discoverPersonas). The runner's persona manifest
// lists exactly these (#6158).
type RegisteredPersona struct {
	Name string
	// SkillDir is the skill directory the file was found in, as passed in.
	SkillDir string
	// Path is the file's host path.
	Path string
	// Data is the file's raw bytes.
	Data []byte
	// Frontmatter is the raw YAML between the fences; nil without one.
	Frontmatter []byte
}

// SkippedPersona is a sub-agents/*.md file discovery did not register.
type SkippedPersona struct {
	Path   string
	Reason string
}

// String is the warning pi prints for a skipped persona.
func (s SkippedPersona) String() string {
	return "persona " + s.Path + " skipped: " + s.Reason
}

// DiscoverPersonas runs the pi/codex persona discovery over skillDirs for
// the agent agentName and returns the registered personas, in the order
// discovery returns them (sorted by name), and the skipped files.
func DiscoverPersonas(skillDirs []string, agentName string) ([]RegisteredPersona, []SkippedPersona) {
	// discoverPersonas never returns a non-nil error.
	personas, skipped, _ := discoverPersonas(skillDirs, agentName)
	out := make([]RegisteredPersona, len(personas))
	for i, p := range personas {
		out[i] = RegisteredPersona{
			Name:        p.Name,
			SkillDir:    p.skillDir,
			Path:        p.path,
			Data:        p.data,
			Frontmatter: p.Frontmatter,
		}
	}
	var skippedOut []SkippedPersona
	for _, sk := range skipped {
		skippedOut = append(skippedOut, SkippedPersona{Path: sk.Path, Reason: sk.Reason})
	}
	return out, skippedOut
}
