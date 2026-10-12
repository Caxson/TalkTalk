// Package llm streams chat completions from language model providers.
package llm

import (
	"context"
	"iter"
)

// Role of a chat message author.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of chat history.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Streamer produces the assistant reply as incremental text deltas.
//
// The sequence yields ("", err) at most once and then stops. Breaking out of
// the loop early, or cancelling ctx, releases the underlying connection.
type Streamer interface {
	Stream(ctx context.Context, messages []Message) iter.Seq2[string, error]
}
