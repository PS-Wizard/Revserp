package ai

// ChatStreamDiagnostic contains counts and fixed labels only, never provider payloads.
type ChatStreamDiagnostic struct {
	Stage          string
	Cause          string
	ErrorCode      string
	HTTPStatus     int
	FinishReason   string
	Chunks         int
	TextBytes      int
	ReasoningBytes int
	ToolCalls      int
	Usage          Usage
	HasUsage       bool
}
