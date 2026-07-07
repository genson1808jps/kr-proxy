package proxy

import (
	"strings"
	"testing"
)

func TestDetectTruncationEmptyWriteTool(t *testing.T) {
	info := DetectTruncation("Write", "c1", "", nil)
	if !info.IsTruncated || info.TruncationType != TruncationTypeEmptyInput {
		t.Fatalf("expected empty_input truncation, got %+v", info)
	}
}

func TestDetectTruncationInvalidJSON(t *testing.T) {
	raw := `{"file_path": "test.txt", "content": "hello`
	info := DetectTruncation("Write", "c1", raw, nil)
	if !info.IsTruncated || info.TruncationType != TruncationTypeInvalidJSON {
		t.Fatalf("expected invalid_json truncation, got %+v", info)
	}
}

func TestDetectTruncationMissingFields(t *testing.T) {
	parsed := map[string]interface{}{"file_path": "test.txt"}
	info := DetectTruncation("Write", "c1", `{"file_path": "test.txt"}`, parsed)
	if !info.IsTruncated || info.TruncationType != TruncationTypeMissingFields {
		t.Fatalf("expected missing_fields truncation, got %+v", info)
	}
}

func TestDetectTruncationValidWrite(t *testing.T) {
	parsed := map[string]interface{}{"file_path": "test.txt", "content": "hello"}
	info := DetectTruncation("Write", "c1", `{"file_path": "test.txt", "content": "hello"}`, parsed)
	if info.IsTruncated {
		t.Fatalf("expected no truncation, got %+v", info)
	}
}

func TestDetectTruncationCommandEitherKey(t *testing.T) {
	parsed := map[string]interface{}{"cmd": "echo hello"}
	info := DetectTruncation("Bash", "c2", `{"cmd":"echo hello"}`, parsed)
	if info.IsTruncated {
		t.Fatalf("expected cmd-only Bash input to be valid, got %+v", info)
	}
}

func TestCompressToolsIfNeededWithinTarget(t *testing.T) {
	tools, _ := convertClaudeTools([]ClaudeTool{{
		Name:        "read_file",
		Description: "Read a file",
		InputSchema: map[string]interface{}{"type": "object"},
	}})
	got := compressToolsIfNeeded(tools)
	if len(got) != 1 || got[0].ToolSpecification.Name == "" {
		t.Fatalf("unexpected tools: %+v", got)
	}
}

func TestCompressToolsIfNeededReducesLargePayload(t *testing.T) {
	longDesc := strings.Repeat("x", 8000)
	claudeTools := make([]ClaudeTool, 0, 6)
	for i := 0; i < 6; i++ {
		claudeTools = append(claudeTools, ClaudeTool{
			Name:        "tool_" + string(rune('a'+i)),
			Description: longDesc,
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": strings.Repeat("y", 2000),
					},
				},
			},
		})
	}
	tools, _ := convertClaudeTools(claudeTools)

	if size := calculateToolsSize(tools); size <= ToolCompressionTargetSize {
		t.Fatalf("test setup should exceed target, got %d bytes", size)
	}

	got := compressToolsIfNeeded(tools)
	if size := calculateToolsSize(got); size > ToolCompressionTargetSize {
		t.Fatalf("expected compressed size <= %d, got %d", ToolCompressionTargetSize, size)
	}
}

func TestChatOnlyStripsTools(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-opus-4.8",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hi"},
		},
		Tools: []ClaudeTool{{Name: "read_file", Description: "read"}},
	}
	payload := ClaudeToKiro(req, false, false, true)
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil && len(ctx.Tools) > 0 {
		t.Fatalf("expected no tools in chat-only mode, got %d", len(ctx.Tools))
	}
}
