package harnessdispatch

// ExecutionRef is the matrix payload consumed by harness-run and fullsend run.
type ExecutionRef struct {
	Agent         string `json:"agent"`
	Role          string `json:"role"`
	SourceRepo    string `json:"source_repo"`
	EventType     string `json:"event_type"`
	EventPayload  string `json:"event_payload"`
	TriggerSource string `json:"trigger_source,omitempty"`
	StatusRepo    string `json:"status_repo"`
	StatusNumber  string `json:"status_number"`
}
