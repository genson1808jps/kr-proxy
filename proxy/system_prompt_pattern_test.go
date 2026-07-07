package proxy

import (
	"strings"
	"testing"
)

func TestFirstTurnEmbedsSystemPromptWithBoundaryMarkers(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1024,
		System:    "You are helpful.",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}

	payload := ClaudeToKiro(req, false, false, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content

	if !strings.Contains(cur, "--- SYSTEM PROMPT ---") || !strings.Contains(cur, "--- END SYSTEM PROMPT ---") {
		t.Fatalf("expected boundary markers in current message, got %q", cur)
	}
	if !strings.Contains(cur, "You are helpful.") {
		t.Fatalf("expected system text in current message, got %q", cur)
	}
	if !strings.Contains(cur, "[Context: Current time is") {
		t.Fatalf("expected timestamp context, got %q", cur)
	}
	if !strings.HasSuffix(strings.TrimSpace(cur), "hello") {
		t.Fatalf("expected user message after system block, got %q", cur)
	}
	if len(payload.ConversationState.History) != 0 {
		t.Fatalf("expected no history priming on first turn, got %d entries", len(payload.ConversationState.History))
	}
}

func TestEffectiveSystemPromptForTurn(t *testing.T) {
	if effectiveSystemPromptForTurn("You are helpful.", 2, false) != "" {
		t.Fatal("expected no system on multi-turn without force inject")
	}
	if effectiveSystemPromptForTurn("new rules", 2, true) != "new rules" {
		t.Fatal("expected force inject on continuation instructions")
	}
}

func TestMultiTurnSkipsSystemReinjection(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1024,
		System:    "You are helpful.",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "second"},
		},
	}

	payload := ClaudeToKiro(req, false, false, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content

	if strings.Contains(cur, "--- SYSTEM PROMPT ---") {
		t.Fatalf("expected no system re-injection on multi-turn, got %q", cur)
	}
	if cur != "second" {
		t.Fatalf("expected bare user message, got %q", cur)
	}
}

func TestOpenAIFirstTurnEmbedsSystemPrompt(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "system", Content: "backend rules"},
			{Role: "user", Content: "run"},
		},
		ToolChoice: "required",
	}

	payload := OpenAIToKiro(req, false, false, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content

	if !strings.Contains(cur, "--- SYSTEM PROMPT ---") {
		t.Fatalf("expected system boundary in current message, got %q", cur)
	}
	if !strings.Contains(cur, "backend rules") {
		t.Fatalf("expected filtered system in current message, got %q", cur)
	}
	if !strings.Contains(cur, "MUST use at least one") {
		t.Fatalf("expected tool_choice hint in system block, got %q", cur)
	}
}
