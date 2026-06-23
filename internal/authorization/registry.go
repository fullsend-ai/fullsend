package authorization

// Gates returns all registered authorization gates.
func Gates() []Gate {
	return append([]Gate(nil), gates...)
}

var workflowChangeGate = Gate{
	Name:         "workflow-change",
	NeededLabel:  "workflow-change-needed",
	AllowedLabel: "workflow-change-allowed",
	FilePatterns: WorkflowFilePatterns(),
	MintElevation: map[string]string{
		"workflows": "write",
	},
}

// GateByName returns the gate with the given name, or nil if unknown.
func GateByName(name string) *Gate {
	for _, g := range gates {
		if g.Name == name {
			gate := g
			return &gate
		}
	}
	return nil
}

var gates = []Gate{workflowChangeGate}
