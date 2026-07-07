package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	toolCallStartTag     = "<tool_call>"
	toolCallEndTag       = "</tool_call>"
	toolResponseStartTag = "<tool_response>"
	toolResponseEndTag   = "</tool_response>"
)

var (
	toolCallStartPattern     = regexp.MustCompile(`(?i)<tool_call>`)
	toolCallEndPattern       = regexp.MustCompile(`(?i)</tool_call>`)
	toolResponseStartPattern = regexp.MustCompile(`(?i)<tool_response>`)
	toolResponseEndPattern   = regexp.MustCompile(`(?i)</tool_response>`)
	pollutedNarrationPattern = regexp.MustCompile(`(?i)(?:\[Called tool [^\]]*\]|<tool_call>[\s\S]*?</tool_call>|<tool_response>[\s\S]*?</tool_response>)`)
)

// ToolNarrationStreamFilter suppresses tool narration markup from streamed assistant
// text while holding back partial tag prefixes at chunk boundaries.
type ToolNarrationStreamFilter struct {
	pending           strings.Builder
	emitted           int
	processedToolKeys map[string]bool
}

func (f *ToolNarrationStreamFilter) initMaps() {
	if f.processedToolKeys == nil {
		f.processedToolKeys = map[string]bool{}
	}
}

// Process returns newly safe assistant text (delta only) and any tool calls
// completed in this chunk. Cursor/Kiro often embed <tool_call> blocks in text
// using {"name","params"} instead of OpenAI tool_calls events.
func (f *ToolNarrationStreamFilter) Process(chunk string) (safeDelta string, newTools []KiroToolUse) {
	if chunk == "" {
		return "", nil
	}
	f.initMaps()

	f.pending.WriteString(chunk)
	full := f.pending.String()
	cleaned, newTools := SanitizeToolNarrationContent(full, f.processedToolKeys)

	safe, hold := holdIncompleteToolNarrationSuffix(cleaned)
	if len(safe) > f.emitted {
		safeDelta = safe[f.emitted:]
		f.emitted = len(safe)
	} else if len(safe) < f.emitted {
		f.emitted = len(safe)
	}

	f.pending.Reset()
	f.pending.WriteString(hold)
	return safeDelta, newTools
}

func dropTrailingIncompleteToolNarration(text string) string {
	safe, hold := holdIncompleteToolNarrationSuffix(text)
	if hold != "" {
		return strings.TrimSpace(safe)
	}
	return text
}

func (f *ToolNarrationStreamFilter) Flush() string {
	if f.pending.Len() == 0 {
		return ""
	}
	f.initMaps()

	full := f.pending.String()
	cleaned, _ := SanitizeToolNarrationContent(full, f.processedToolKeys)
	cleaned = dropTrailingIncompleteToolNarration(cleaned)
	safe, _ := holdIncompleteToolNarrationSuffix(cleaned)

	var delta string
	if len(safe) > f.emitted {
		delta = safe[f.emitted:]
	}
	f.pending.Reset()
	f.emitted = 0
	return strings.TrimSpace(delta)
}

func sanitizeStreamContentDelta(content string) string {
	if content == "" {
		return ""
	}
	return strings.TrimSpace(stripCompleteToolNarrationBlocks(content))
}

func stripCompleteToolNarrationBlocks(text string) string {
	if text == "" {
		return text
	}
	prev := ""
	for prev != text {
		prev = text
		text = stripToolResponseBlocks(text)
		text = stripXMLToolCallBlocks(text)
		text = pollutedNarrationPattern.ReplaceAllString(text, "")
		text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	}
	return text
}

func stripToolResponseBlocks(text string) string {
	for {
		start := toolResponseStartPattern.FindStringIndex(text)
		if start == nil {
			return text
		}
		rest := text[start[0]:]
		end := toolResponseEndPattern.FindStringIndex(rest)
		if end == nil {
			return text
		}
		endTag := toolResponseEndPattern.FindString(rest[end[0]:])
		endIdx := start[0] + end[0] + len(endTag)
		text = text[:start[0]] + text[endIdx:]
	}
}

func stripXMLToolCallBlocks(text string) string {
	for {
		start := toolCallStartPattern.FindStringIndex(text)
		if start == nil {
			return text
		}
		rest := text[start[0]:]
		end := toolCallEndPattern.FindStringIndex(rest)
		if end == nil {
			return text
		}
		endTag := toolCallEndPattern.FindString(rest[end[0]:])
		endIdx := start[0] + end[0] + len(endTag)
		text = text[:start[0]] + text[endIdx:]
	}
}

func holdIncompleteToolNarrationSuffix(text string) (safe, hold string) {
	candidates := []int{
		strings.LastIndex(strings.ToLower(text), "<tool_call"),
		strings.LastIndex(strings.ToLower(text), "<tool_response"),
		strings.LastIndex(strings.ToLower(text), "</tool_call"),
		strings.LastIndex(strings.ToLower(text), "</tool_response"),
		strings.LastIndex(text, "[Called"),
	}
	holdFrom := -1
	for _, idx := range candidates {
		if idx < 0 {
			continue
		}
		suffix := text[idx:]
		if isIncompleteToolNarrationSuffix(suffix) {
			if holdFrom < 0 || idx < holdFrom {
				holdFrom = idx
			}
		}
	}
	if holdFrom >= 0 {
		return text[:holdFrom], text[holdFrom:]
	}
	return text, ""
}

func isIncompleteToolNarrationSuffix(suffix string) bool {
	lower := strings.ToLower(suffix)
	switch {
	case strings.HasPrefix(lower, "<tool_call>") && !strings.Contains(lower, "</tool_call>"):
		return true
	case strings.HasPrefix(lower, "<tool_response>") && !strings.Contains(lower, "</tool_response>"):
		return true
	case strings.HasPrefix(lower, "<tool_call") && !strings.HasPrefix(lower, "<tool_call>"):
		return true
	case strings.HasPrefix(lower, "<tool_response") && !strings.HasPrefix(lower, "<tool_response>"):
		return true
	case strings.HasPrefix(lower, "</tool_call") && !strings.HasPrefix(lower, "</tool_call>"):
		return true
	case strings.HasPrefix(lower, "</tool_response") && !strings.HasPrefix(lower, "</tool_response>"):
		return true
	case strings.HasPrefix(suffix, "[Called") && !strings.Contains(suffix, "]"):
		return true
	}
	return false
}

// ParseXMLStyleToolCalls extracts <tool_call>{...}</tool_call> blocks from text.
func ParseXMLStyleToolCalls(text string, processedIDs map[string]bool) (string, []KiroToolUse) {
	if !toolCallStartPattern.MatchString(text) {
		return text, nil
	}

	var toolUses []KiroToolUse
	cleanText := text

	for {
		start := toolCallStartPattern.FindStringIndex(cleanText)
		if start == nil {
			break
		}
		rest := cleanText[start[0]:]
		end := toolCallEndPattern.FindStringIndex(rest)
		if end == nil {
			break
		}

		matchStart := start[0]
		endTag := toolCallEndPattern.FindString(rest[end[0]:])
		matchEnd := start[0] + end[0] + len(endTag)
		startTag := toolCallStartPattern.FindString(rest)
		inner := strings.TrimSpace(rest[len(startTag) : end[0]])

		tu, ok := parseXMLToolCallPayload(inner)
		if ok {
			dedupeKey := tu.Name + ":" + mustJSONString(tu.Input)
			if processedIDs != nil {
				if processedIDs[dedupeKey] {
					cleanText = cleanText[:matchStart] + cleanText[matchEnd:]
					continue
				}
				processedIDs[dedupeKey] = true
			}
			toolUses = append(toolUses, tu)
		}

		cleanText = cleanText[:matchStart] + cleanText[matchEnd:]
	}

	return strings.TrimSpace(stripToolResponseBlocks(cleanText)), toolUses
}

func parseXMLToolCallPayload(inner string) (KiroToolUse, bool) {
	repaired := RepairJSON(strings.TrimSpace(inner))
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(repaired), &payload); err != nil {
		return KiroToolUse{}, false
	}

	name, _ := payload["name"].(string)
	if name == "" {
		return KiroToolUse{}, false
	}

	input := firstToolCallArguments(payload)
	if input == nil {
		input = map[string]interface{}{}
	}

	return KiroToolUse{
		ToolUseID: "toolu_" + uuid.New().String()[:12],
		Name:      name,
		Input:     input,
	}, true
}

func firstToolCallArguments(payload map[string]interface{}) map[string]interface{} {
	for _, key := range []string{"arguments", "params", "input", "parameters"} {
		if args := normalizeToolCallArguments(payload[key]); len(args) > 0 {
			return args
		}
	}
	return normalizeToolCallArguments(payload["arguments"])
}

func normalizeToolCallArguments(raw interface{}) map[string]interface{} {
	switch v := raw.(type) {
	case map[string]interface{}:
		if len(v) == 0 {
			return nil
		}
		return v
	case string:
		repaired := RepairJSON(strings.TrimSpace(v))
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(repaired), &parsed); err == nil && len(parsed) > 0 {
			return parsed
		}
	}
	return nil
}

// SanitizeToolNarrationContent strips fake tool narration and extracts real tool calls.
func SanitizeToolNarrationContent(text string, processedIDs map[string]bool) (string, []KiroToolUse) {
	if text == "" {
		return text, nil
	}

	var allTools []KiroToolUse
	cleaned, xmlTools := ParseXMLStyleToolCalls(text, processedIDs)
	allTools = append(allTools, xmlTools...)
	cleaned = stripToolResponseBlocks(cleaned)
	cleaned, calledTools := ParseEmbeddedToolCalls(cleaned, processedIDs)
	allTools = append(allTools, calledTools...)
	cleaned = stripPollutedToolCallText(cleaned)
	return strings.TrimSpace(cleaned), deduplicateToolUses(allTools)
}

func deduplicateToolUses(toolUses []KiroToolUse) []KiroToolUse {
	seenIDs := make(map[string]bool)
	seenContent := make(map[string]bool)
	unique := make([]KiroToolUse, 0, len(toolUses))
	for _, tu := range toolUses {
		if seenIDs[tu.ToolUseID] {
			continue
		}
		contentKey := tu.Name + ":" + mustJSONString(tu.Input)
		if seenContent[contentKey] {
			continue
		}
		seenIDs[tu.ToolUseID] = true
		seenContent[contentKey] = true
		unique = append(unique, tu)
	}
	return unique
}

func mustJSONString(v map[string]interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// emitEmbeddedOpenAIToolCalls sends tool_calls SSE chunks for tools parsed from text.
func emitEmbeddedOpenAIToolCalls(
	w http.ResponseWriter,
	flusher http.Flusher,
	chatID, model string,
	responseStarted *bool,
	toolCallIndex *int,
	toolIndexByID map[string]int,
	openAIToolStarted map[string]bool,
	toolCalls *[]ToolCall,
	tools []KiroToolUse,
) {
	for _, tu := range tools {
		args := MarshalToolUseArguments(tu)
		tc := ToolCall{ID: tu.ToolUseID, Type: "function"}
		tc.Function.Name = tu.Name
		tc.Function.Arguments = args
		*toolCalls = append(*toolCalls, tc)

		if openAIToolStarted[tu.ToolUseID] {
			continue
		}
		openAIToolStarted[tu.ToolUseID] = true
		idx := *toolCallIndex
		*toolCallIndex++
		toolIndexByID[tu.ToolUseID] = idx

		startDelta := map[string]interface{}{"tool_calls": []map[string]interface{}{{
			"index": idx, "id": tu.ToolUseID, "type": "function",
			"function": map[string]string{"name": tu.Name, "arguments": ""},
		}}}
		if !*responseStarted {
			startDelta["role"] = "assistant"
		}
		chunk := map[string]interface{}{
			"id": chatID, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []map[string]interface{}{{"index": 0, "delta": startDelta, "finish_reason": nil}},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", string(data))
		flusher.Flush()
		*responseStarted = true

		argChunk := map[string]interface{}{
			"id": chatID, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{{
						"index":    idx,
						"function": map[string]string{"arguments": args},
					}},
				},
				"finish_reason": nil,
			}},
		}
		data, _ = json.Marshal(argChunk)
		fmt.Fprintf(w, "data: %s\n\n", string(data))
		flusher.Flush()
	}
}
