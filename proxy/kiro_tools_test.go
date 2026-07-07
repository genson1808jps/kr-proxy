package proxy

import (
	"strings"
	"testing"
)

func TestRepairJSONAddsMissingClosingBrace(t *testing.T) {
	got := RepairJSON(`{"k":1`)
	if got != `{"k":1}` {
		t.Fatalf("RepairJSON() = %q", got)
	}
}

func TestRepairJSONValidUnchanged(t *testing.T) {
	raw := `{"ok":true}`
	if RepairJSON(raw) != raw {
		t.Fatalf("expected valid JSON unchanged")
	}
}

func TestParseEmbeddedToolCalls(t *testing.T) {
	text := `Done. [Called read_file with args: {"path":"x"}] thanks`
	cleaned, uses := ParseEmbeddedToolCalls(text, nil)
	if strings.Contains(cleaned, "[Called") {
		t.Fatalf("expected cleaned text, got %q", cleaned)
	}
	if len(uses) != 1 || uses[0].Name != "read_file" {
		t.Fatalf("expected read_file tool, got %+v", uses)
	}
	if uses[0].Input["path"] != "x" {
		t.Fatalf("expected path x, got %#v", uses[0].Input)
	}
}

func TestParseModelThinkingAndAgentic(t *testing.T) {
	model, thinking, agentic, chatOnly := ParseModelThinkingAndAgentic("claude-opus-4.8-agentic", "-thinking")
	if !agentic || chatOnly || model != "claude-opus-4.8" {
		t.Fatalf("expected agentic stripped model, got model=%q agentic=%v chatOnly=%v", model, agentic, chatOnly)
	}
	model, thinking, agentic, chatOnly = ParseModelThinkingAndAgentic("claude-opus-4.8-thinking", "-thinking")
	if !thinking || agentic || chatOnly || model != "claude-opus-4.8" {
		t.Fatalf("expected thinking stripped model, got model=%q thinking=%v agentic=%v chatOnly=%v", model, thinking, agentic, chatOnly)
	}
	model, _, _, chatOnly = ParseModelThinkingAndAgentic("claude-opus-4.8-chat", "-thinking")
	if !chatOnly || model != "claude-opus-4.8" {
		t.Fatalf("expected chat-only stripped model, got model=%q chatOnly=%v", model, chatOnly)
	}
}

func TestIsThinkingEnabledFromHeader(t *testing.T) {
	h := make(map[string][]string)
	h["Anthropic-Beta"] = []string{"interleaved-thinking-2025-05-14"}
	if !isThinkingEnabledFromHeader(h) {
		t.Fatal("expected interleaved-thinking header to enable thinking")
	}
}
