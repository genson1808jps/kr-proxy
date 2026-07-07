package proxy

import (
	"strings"
	"testing"
)

func TestParseXMLStyleToolCalls(t *testing.T) {
	text := `Intro text.

<tool_call> {"name": "list_directory", "arguments": {"path": "/home/genson1808/poo"}} </tool_call>

<tool_response> [ { "name": "backend", "type": "directory" } ] </tool_response>

More text.`

	cleaned, uses := ParseXMLStyleToolCalls(text, nil)
	if len(uses) != 1 {
		t.Fatalf("expected 1 tool use, got %d: %+v", len(uses), uses)
	}
	if uses[0].Name != "list_directory" {
		t.Fatalf("expected list_directory, got %q", uses[0].Name)
	}
	if uses[0].Input["path"] != "/home/genson1808/poo" {
		t.Fatalf("unexpected input: %#v", uses[0].Input)
	}
	if strings.Contains(cleaned, "<tool_call>") || strings.Contains(cleaned, "<tool_response>") {
		t.Fatalf("expected markup stripped from cleaned text, got %q", cleaned)
	}
	if !strings.Contains(cleaned, "Intro text.") || !strings.Contains(cleaned, "More text.") {
		t.Fatalf("expected surrounding prose preserved, got %q", cleaned)
	}
}

func TestSanitizeToolNarrationContentUserExample(t *testing.T) {
	text := `Để phân tích dự án, tôi sẽ khám phá cấu trúc thư mục trước.

<tool_call> {"name": "list_directory", "arguments": {"path": "/home/genson1808/poo"}} </tool_call> <tool_response> [ { "name": "backend", "type": "directory" } ] </tool_response>

<tool_call> {"name": "read_file", "arguments": {"path": "/home/genson1808/poo/backend/app/main.py"}} </tool_call> <tool_response> from fastapi import FastAPI </tool_response>`

	cleaned, uses := SanitizeToolNarrationContent(text, nil)
	if len(uses) != 2 {
		t.Fatalf("expected 2 tool uses, got %d: %+v", len(uses), uses)
	}
	if strings.Contains(strings.ToLower(cleaned), "<tool_call") || strings.Contains(strings.ToLower(cleaned), "<tool_response") {
		t.Fatalf("expected all narration markup removed, got %q", cleaned)
	}
	if strings.Contains(cleaned, "backend") && strings.Contains(cleaned, "FastAPI") {
		t.Fatalf("expected hallucinated tool_response content removed, got %q", cleaned)
	}
}

func TestParseEmbeddedToolCallsWithInputVariant(t *testing.T) {
	text := `[Called tool exec_command with input {"cmd":"pwd"}]`
	cleaned, uses := ParseEmbeddedToolCalls(text, nil)
	if len(uses) != 1 || uses[0].Name != "exec_command" {
		t.Fatalf("expected exec_command tool, got %+v", uses)
	}
	if uses[0].Input["cmd"] != "pwd" {
		t.Fatalf("unexpected input: %#v", uses[0].Input)
	}
	if strings.Contains(cleaned, "[Called") {
		t.Fatalf("expected marker stripped, got %q", cleaned)
	}
}

func TestToolNarrationStreamFilterHoldsPartialTag(t *testing.T) {
	var f ToolNarrationStreamFilter
	out := f.Process("hello <tool_call> {\"name\":\"x\"")
	if out != "hello " {
		t.Fatalf("expected safe prefix only, got %q", out)
	}
	out = f.Process(`,"arguments":{}} </tool_call>`)
	if strings.Contains(out, "<tool_call>") {
		t.Fatalf("expected completed block suppressed, got %q", out)
	}
}

func TestStripPollutedToolCallTextXML(t *testing.T) {
	text := "plan\n<tool_call> {\"name\":\"x\",\"arguments\":{}} </tool_call>\n<tool_response>fake</tool_response>\n"
	got := stripPollutedToolCallText(text)
	if strings.Contains(strings.ToLower(got), "<tool") {
		t.Fatalf("expected xml pollution stripped, got %q", got)
	}
	if got != "plan" {
		t.Fatalf("expected prose only, got %q", got)
	}
}

func TestMergeEmbeddedToolUsesDedupesStructuredAndEmbedded(t *testing.T) {
	existing := []KiroToolUse{{
		ToolUseID: "toolu_1",
		Name:      "read_file",
		Input:     map[string]interface{}{"path": "a"},
	}}
	content := `<tool_call> {"name": "read_file", "arguments": {"path": "a"}} </tool_call>`
	cleaned, merged := mergeEmbeddedToolUses(content, existing)
	if len(merged) != 1 {
		t.Fatalf("expected deduped single tool, got %d", len(merged))
	}
	if cleaned != "" {
		t.Fatalf("expected empty cleaned content, got %q", cleaned)
	}
}
