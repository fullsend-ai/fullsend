package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/skill"
)

const (
	// personasFileName is the manifest's file name on the host (in the run
	// directory) and in the sandbox (next to output-schema.json).
	personasFileName = "personas.json"
	// personasEnvVar names the manifest path for skills and host scripts.
	personasEnvVar = "FULLSEND_PERSONAS_FILE"
	// personasManifestVersion is the manifest schema version.
	personasManifestVersion = 1
)

// remotePersonasPath is the manifest's path inside the sandbox. It sits
// under .fullsend/ outside the target repo checkout, so a PR cannot plant
// one.
var remotePersonasPath = sandbox.SandboxWorkspace + "/.fullsend/" + personasFileName

// personaManifest is the personas.json document (version 1).
type personaManifest struct {
	Version  int              `json:"version"`
	Personas []personaEntry   `json:"personas"`
	Removed  []removedPersona `json:"removed"`
}

// personaEntry describes one registered persona. The optional fields are
// copied verbatim from frontmatter and omitted when absent, null or of the
// wrong type; the runner gives them no meaning.
type personaEntry struct {
	Name            string    `json:"name"`
	Skill           string    `json:"skill"`
	Path            string    `json:"path"`
	Origin          string    `json:"origin"`
	SHA256          string    `json:"sha256"`
	Description     *string   `json:"description,omitempty"`
	Role            *string   `json:"role,omitempty"`
	When            *string   `json:"when,omitempty"`
	Categories      *[]string `json:"categories,omitempty"`
	Always          *bool     `json:"always,omitempty"`
	FailureSeverity *string   `json:"failure_severity,omitempty"`
}

// removedPersona is a sub-agents/ file a null override removed.
type removedPersona struct {
	Skill string `json:"skill"`
	Path  string `json:"path"`
}

// buildPersonaManifest lists exactly the personas the pi and codex
// runtimes register from the staged skills (runtime.DiscoverPersonas):
// files discovery skips are left out with pi's warning, and a name two
// skills declare keeps the first. Only I/O fails it.
func buildPersonaManifest(staged []stagedSkill, agentName string, warn func(string)) (personaManifest, error) {
	m := personaManifest{
		Version:  personasManifestVersion,
		Personas: []personaEntry{},
		Removed:  []removedPersona{},
	}
	byDir := make(map[string]stagedSkill, len(staged))
	for _, s := range staged {
		if _, ok := byDir[s.Dir]; !ok {
			byDir[s.Dir] = s
		}
		skillName := skill.SandboxName(s.Dir)
		for _, rel := range s.Removed {
			// The discovery rule: sub-agents/*.md, no recursion.
			if path.Dir(rel) == "sub-agents" && strings.HasSuffix(rel, ".md") {
				m.Removed = append(m.Removed, removedPersona{Skill: skillName, Path: rel})
			}
		}
	}

	personas, skipped := agentruntime.DiscoverPersonas(stagedSkillDirs(staged), agentName)
	for _, sk := range skipped {
		warn(sk.String())
	}
	for _, p := range personas {
		s, ok := byDir[p.SkillDir]
		if !ok {
			return personaManifest{}, fmt.Errorf("%s: persona %s is not under a staged skill", personasFileName, p.Path)
		}
		rel, err := filepath.Rel(s.Dir, p.Path)
		if err != nil {
			return personaManifest{}, fmt.Errorf("%s: %s: %w", personasFileName, p.Path, err)
		}
		rel = filepath.ToSlash(rel)
		skillName := skill.SandboxName(s.Dir)
		e := personaPassThrough(skillName+"/"+rel, p.Frontmatter, warn)
		sum := sha256.Sum256(p.Data)
		e.Name = p.Name
		e.Skill = skillName
		e.Path = rel
		e.Origin = s.origin(rel)
		e.SHA256 = hex.EncodeToString(sum[:])
		m.Personas = append(m.Personas, e)
	}
	sort.Slice(m.Personas, func(i, j int) bool {
		a, b := m.Personas[i], m.Personas[j]
		if a.Skill != b.Skill {
			return a.Skill < b.Skill
		}
		return a.Name < b.Name
	})
	sort.Slice(m.Removed, func(i, j int) bool {
		if m.Removed[i].Skill != m.Removed[j].Skill {
			return m.Removed[i].Skill < m.Removed[j].Skill
		}
		return m.Removed[i].Path < m.Removed[j].Path
	})
	return m, nil
}

// personaPassThrough copies the optional frontmatter fields. A field that
// is absent or null is omitted; one of the wrong type is omitted with a
// warning naming the file and field.
func personaPassThrough(file string, front []byte, warn func(string)) personaEntry {
	var p personaEntry
	if len(front) == 0 {
		return p
	}
	var fm map[string]any
	if err := yaml.Unmarshal(front, &fm); err != nil {
		// Discovery already parsed this block; keep the entry, drop the fields.
		warn(fmt.Sprintf("%s: %s: frontmatter fields not copied: %v", personasFileName, file, err))
		return p
	}
	wrongType := func(field, want string) {
		warn(fmt.Sprintf("%s: %s: frontmatter field %q is not %s; omitted", personasFileName, file, field, want))
	}
	str := func(field string) *string {
		v := fm[field]
		if v == nil {
			return nil
		}
		s, ok := v.(string)
		if !ok {
			wrongType(field, "a string")
			return nil
		}
		return &s
	}
	p.Description = str("description")
	p.Role = str("role")
	p.When = str("when")
	p.FailureSeverity = str("failure_severity")
	if v := fm["always"]; v != nil {
		if b, ok := v.(bool); ok {
			p.Always = &b
		} else {
			wrongType("always", "a bool")
		}
	}
	if v := fm["categories"]; v != nil {
		list, ok := v.([]any)
		cats := make([]string, 0, len(list))
		for _, c := range list {
			s, isStr := c.(string)
			if !isStr {
				ok = false
				break
			}
			cats = append(cats, s)
		}
		if ok {
			p.Categories = &cats
		} else {
			wrongType("categories", "a list of strings")
		}
	}
	return p
}

// writePersonaManifest builds the manifest for the staged skills and
// writes it to runDir/personas.json, returning the absolute path.
func writePersonaManifest(staged []stagedSkill, runDir, agentName string, warn func(string)) (string, error) {
	m, err := buildPersonaManifest(staged, agentName, warn)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding %s: %w", personasFileName, err)
	}
	dest, err := filepath.Abs(filepath.Join(runDir, personasFileName))
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", personasFileName, err)
	}
	if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", personasFileName, err)
	}
	return dest, nil
}

// withPersonasFile appends FULLSEND_PERSONAS_FILE=<runDir>/personas.json
// to a host script environment, dropping any earlier value (for example
// one runner_env set) so the runner's path always wins.
func withPersonasFile(env []string, runDir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, personasEnvVar+"=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, personasEnvVar+"="+filepath.Join(runDir, personasFileName))
}
