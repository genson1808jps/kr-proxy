package proxy

import (
	"strings"
	"testing"
)

func TestResolveOpenAIThinkingFromReasoningEffort(t *testing.T) {
	req := &OpenAIRequest{Model: "claude-sonnet-4.5", ReasoningEffort: "high"}
	_, thinking := resolveOpenAIThinkingMode(req, "-thinking")
	if !thinking {
		t.Fatal("expected thinking enabled from reasoning_effort")
	}

	req.ReasoningEffort = "none"
	_, thinking = resolveOpenAIThinkingMode(req, "-thinking")
	if thinking {
		t.Fatal("expected reasoning_effort none to disable thinking")
	}
}

func TestOpenAIToolChoiceNoneOmitsToolsFromPayload(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "hi"},
		},
		Tools: []OpenAITool{
			{Type: "function", Function: struct {
				Name        string      `json:"name"`
				Description string      `json:"description"`
				Parameters  interface{} `json:"parameters"`
			}{Name: "read_file", Parameters: map[string]interface{}{"type": "object"}}},
		},
		ToolChoice: "none",
	}

	payload := OpenAIToKiro(req, false, false, false)
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil && len(ctx.Tools) > 0 {
		t.Fatal("expected no tools when tool_choice is none")
	}
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(cur, "Do NOT use any tools") {
		t.Fatalf("expected tool_choice none hint in system block, got %q", cur)
	}
}

func TestOpenAIToolChoiceRequiredInjectsHint(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "run"},
		},
		ToolChoice: "required",
	}
	payload := OpenAIToKiro(req, false, false, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(cur, "MUST use at least one") {
		t.Fatalf("expected required tool_choice hint, got %q", cur)
	}
}

func TestOpenAIMaxTokensMinusOneMapsToKiroMax(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "hi"},
		},
		MaxTokens: -1,
	}
	payload := OpenAIToKiro(req, false, false, false)
	if payload.InferenceConfig == nil || payload.InferenceConfig.MaxTokens != kiroMaxOutputTokens {
		t.Fatalf("expected max tokens %d, got %+v", kiroMaxOutputTokens, payload.InferenceConfig)
	}
}

func TestOpenAIToolNameMapRestoredViaPayload(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "hi"},
		},
		Tools: []OpenAITool{
			{Type: "function", Function: struct {
				Name        string      `json:"name"`
				Description string      `json:"description"`
				Parameters  interface{} `json:"parameters"`
			}{Name: "exec_command", Parameters: map[string]interface{}{"type": "object"}}},
		},
	}
	payload := OpenAIToKiro(req, false, false, false)
	if payload.ToolNameMap == nil || payload.ToolNameMap["execCommand"] != "exec_command" {
		t.Fatalf("expected execCommand->exec_command map, got %#v", payload.ToolNameMap)
	}
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.Tools) != 1 || ctx.Tools[0].ToolSpecification.Name != "execCommand" {
		t.Fatalf("expected sanitized tool name execCommand, got %+v", ctx)
	}
}

func TestParseToolArgumentsToMap(t *testing.T) {
	got := parseToolArgumentsToMap(`{"path":"a.txt"}`)
	if got["path"] != "a.txt" {
		t.Fatalf("expected object parse, got %#v", got)
	}
	got = parseToolArgumentsToMap(`"hello"`)
	if got["value"] != "hello" {
		t.Fatalf("expected scalar wrapper, got %#v", got)
	}
	got = parseToolArgumentsToMap(`not-json`)
	if got["raw"] != "not-json" {
		t.Fatalf("expected raw fallback, got %#v", got)
	}
}

func TestClaudeToolChoiceNoneOmitsToolsFromPayload(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1024,
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hi"},
		},
		Tools: []ClaudeTool{
			{Name: "read_file", Description: "read", InputSchema: map[string]interface{}{"type": "object"}},
		},
		ToolChoice: map[string]interface{}{"type": "none"},
	}

	payload := ClaudeToKiro(req, false, false, false)
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil && len(ctx.Tools) > 0 {
		t.Fatal("expected no tools when tool_choice.type is none")
	}
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(cur, "Do NOT use any tools") {
		t.Fatalf("expected tool_choice none hint in system block, got %q", cur)
	}
}

func TestClaudeToolChoiceAnyInjectsHint(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1024,
		Messages: []ClaudeMessage{
			{Role: "user", Content: "run"},
		},
		ToolChoice: map[string]interface{}{"type": "any"},
	}
	payload := ClaudeToKiro(req, false, false, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(cur, "MUST use at least one") {
		t.Fatalf("expected any tool_choice hint, got %q", cur)
	}
}

func TestClaudeToolChoiceNamedToolInjectsHint(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1024,
		Messages: []ClaudeMessage{
			{Role: "user", Content: "run"},
		},
		ToolChoice: map[string]interface{}{"type": "tool", "name": "read_file"},
	}
	payload := ClaudeToKiro(req, false, false, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(cur, "read_file") {
		t.Fatalf("expected named tool hint, got %q", cur)
	}
}

func TestOpenAISystemPromptPassesThroughPromptFilters(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "system", Content: "custom backend instructions"},
			{Role: "user", Content: "hi"},
		},
	}
	payload := OpenAIToKiro(req, false, false, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(cur, "custom backend instructions") {
		t.Fatalf("expected system prompt in current message, got %q", cur)
	}
}
