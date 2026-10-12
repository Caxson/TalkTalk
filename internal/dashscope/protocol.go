// Package dashscope speaks Alibaba Cloud Model Studio's duplex WebSocket task
// protocol, shared by real-time ASR and CosyVoice TTS:
//
//	client: run-task → (binary audio | continue-task text)* → finish-task
//	server: task-started → (result-generated | binary audio)* → task-finished | task-failed
//
// One connection carries one task at a time; tasks may run back to back.
// After task-failed the server considers the connection unusable.
package dashscope

import "encoding/json"

// DefaultURL is the Beijing inference endpoint.
const DefaultURL = "wss://dashscope.aliyuncs.com/api-ws/v1/inference"

type header struct {
	Action       string         `json:"action,omitempty"`
	TaskID       string         `json:"task_id"`
	Streaming    string         `json:"streaming,omitempty"`
	Event        string         `json:"event,omitempty"`
	ErrorCode    string         `json:"error_code,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
}

type clientMsg struct {
	Header  header         `json:"header"`
	Payload map[string]any `json:"payload"`
}

type serverMsg struct {
	Header  header `json:"header"`
	Payload struct {
		Output json.RawMessage `json:"output"`
	} `json:"payload"`
}

const (
	evStarted  = "task-started"
	evResult   = "result-generated"
	evFinished = "task-finished"
	evFailed   = "task-failed"
)

// TaskSpec selects the model and its parameters for run-task.
type TaskSpec struct {
	TaskGroup  string // "audio"
	Task       string // "asr" | "tts"
	Function   string // "recognition" | "SpeechSynthesizer"
	Model      string
	Parameters map[string]any
}
