package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AskRequest is one clarifying question posed to the human.
type AskRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// AskFunc asks the human a question and returns their answer. Front-ends that
// have no human on the other end (MCP, delegates, pipes) leave Env.Ask nil.
type AskFunc func(ctx context.Context, req AskRequest) (string, error)

// askUserTool lets the model ask instead of guessing.
//
// The tool is deliberately boring: no free-form terminal UI, no multi-turn
// interview. It exists so a wrong assumption costs one sentence instead of a
// wasted turn, and so an unattended run can tell the difference between "the
// user chose this" and "the model decided this".
func askUserTool() Tool {
	return Tool{
		Name:     "ask_user",
		ReadOnly: true,
		Description: "Ask the human one clarifying question when a wrong guess would waste real work " +
			"(ambiguous target, destructive choice, missing intent). Provide 2-4 short options when the answer is a choice. " +
			"Do not use it for questions the repository can answer — read the code first. " +
			"Unattended runs have no human: the tool tells you to proceed on a stated assumption.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "One specific question, answerable in a sentence."},
    "options": {"type": "array", "items": {"type": "string"}, "description": "Optional short choices shown as a numbered list."}
  },
  "required": ["question"]
}`),
		Handler: handleAskUser,
	}
}

func handleAskUser(ctx context.Context, raw json.RawMessage) (string, error) {
	var a AskRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	a.Question = strings.TrimSpace(a.Question)
	if a.Question == "" {
		return "", errors.New("question is required")
	}
	if len(a.Options) > 6 {
		a.Options = a.Options[:6]
	}
	env := currentEnv(ctx)
	if env.Ask == nil {
		return "", errors.New("no interactive user is available (headless, MCP or delegate run); " +
			"proceed with the most reasonable assumption and state it in your answer")
	}
	answer, err := env.Ask(ctx, a)
	if err != nil {
		return "", fmt.Errorf("could not ask the user: %w", err)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "the user gave no answer; use your judgment and state the assumption", nil
	}
	return "user answer: " + answer, nil
}
