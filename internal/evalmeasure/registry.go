package evalmeasure

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Registry is an agent-specific measurement manifest (owned by agents repo).
type Registry struct {
	Agent        string            `yaml:"agent"`
	Measurements []MeasurementSpec `yaml:"measurements"`
}

// MeasurementSpec selects a framework scorer.
type MeasurementSpec struct {
	ID      string `yaml:"id"`
	Scorer  string `yaml:"scorer"`
	Name    string `yaml:"name"` // optional display override; default = Scorer
	Version int    `yaml:"version"`
}

// LoadRegistry loads a measurement manifest YAML file.
func LoadRegistry(path string) (Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, err
	}
	var reg Registry
	if err := yaml.Unmarshal(b, &reg); err != nil {
		return Registry{}, err
	}
	if reg.Agent == "" {
		return Registry{}, fmt.Errorf("manifest %s: agent is required", path)
	}
	for i, m := range reg.Measurements {
		if m.ID == "" {
			return Registry{}, fmt.Errorf("manifest %s: measurements[%d].id is required", path, i)
		}
		if m.Scorer == "" {
			return Registry{}, fmt.Errorf("manifest %s: measurements[%d].scorer is required", path, i)
		}
		if m.Version <= 0 {
			return Registry{}, fmt.Errorf("manifest %s: measurements[%d].version must be >= 1", path, i)
		}
		if strings.ContainsAny(m.ID, "|\n") || strings.ContainsAny(m.Scorer, "|\n") || strings.ContainsAny(m.Name, "|\n") {
			return Registry{}, fmt.Errorf("manifest %s: measurements[%d].id, .scorer, and .name must not contain pipe or newline", path, i)
		}
	}
	return reg, nil
}

func (m MeasurementSpec) versionString() string {
	return fmt.Sprintf("%s@%d", m.ID, m.Version)
}

func (m MeasurementSpec) evalName() string {
	if m.Name != "" {
		return m.Name
	}
	return m.Scorer
}

// ScoreTrace runs enabled measurements for traces matching the manifest agent.
// An empty or UnknownSentinel agent name still scores so identity=fail is
// recorded (EM-001 exists to catch missing identity). A different, known
// agent name is skipped. Unknown scorer strings write a skip row rather
// than silent-no-op or a fail that mixes into pass-rate (a newer agents@v0
// manifest can name a scorer this binary does not implement yet).
func ScoreTrace(tr Trace, reg Registry) []EvaluationResult {
	name := tr.AgentName()
	if name != "" && name != UnknownSentinel && !strings.EqualFold(name, reg.Agent) {
		return nil
	}
	var out []EvaluationResult
	for _, m := range reg.Measurements {
		switch m.Scorer {
		case ScorerFitness:
			out = append(out, ScoreFitnessNamed(tr, m.evalName(), m.versionString()))
		case ScorerRunHealth:
			out = append(out, ScoreRunHealthNamed(tr, m.evalName(), m.versionString()))
		default:
			out = append(out, EvaluationResult{
				Name:        m.evalName(),
				Label:       LabelSkip,
				Explanation: "unknown scorer: " + m.Scorer,
				TraceID:     tr.TraceID,
				Agent:       name,
				Version:     m.versionString(),
			})
		}
	}
	return out
}
