package dialog

import "time"

// State of the conversation from the assistant's point of view.
type State string

const (
	StateListening State = "listening" // idle; the user may speak
	StateThinking  State = "thinking"  // reply in progress, nothing audible yet
	StateSpeaking  State = "speaking"  // reply audio queued or playing
)

// Event is pushed to the client (UI) as the conversation evolves. Exactly
// one of the payload fields is set, matching Type.
type Event struct {
	// state       assistant state changed (State)
	// user        live recognition, interim or final (Text, Final)
	// turn        a reply to the user's words began (Turn, Text = what it answers)
	// retracted   a reply was abandoned before any audio; its turn is void
	// assistant   a reply segment started playing (Turn, Text)
	// backchannel the user said something short over the reply (Turn, Text)
	// interrupted the user cut the reply off (Turn)
	// metrics     latency breakdown for a reply (Turn, Metrics)
	// avatar      motion frames will follow on the motion channel (Names, FrameRate)
	// error       something failed (Error)
	Type string `json:"type"`

	State     State        `json:"state,omitempty"`
	Text      string       `json:"text,omitempty"`
	Final     bool         `json:"final,omitempty"`
	Turn      int          `json:"turn,omitempty"`
	Metrics   *TurnMetrics `json:"metrics,omitempty"`
	Error     string       `json:"error,omitempty"`
	Names     []string     `json:"names,omitempty"`
	FrameRate int          `json:"frame_rate,omitempty"`
	Timestamp time.Time    `json:"ts"`
}

// TurnMetrics break down the latency of one reply. Durations are measured
// from the moment the user's final transcript arrived.
type TurnMetrics struct {
	Turn int `json:"turn"`
	// SpeechEndToFinal is how long the recognizer took to close the
	// sentence after the user actually stopped talking (audio clock).
	SpeechEndToFinal time.Duration `json:"speech_end_to_final_ns"`
	LLMFirstToken    time.Duration `json:"llm_first_token_ns"`
	TTSFirstAudio    time.Duration `json:"tts_first_audio_ns"`
	// FirstAudioOut is when the first reply frame entered the playout
	// queue; the client hears it one network hop plus jitter buffer later.
	FirstAudioOut time.Duration `json:"first_audio_out_ns"`
	// Total is user speech end to first audio out.
	Total time.Duration `json:"total_ns"`
}
