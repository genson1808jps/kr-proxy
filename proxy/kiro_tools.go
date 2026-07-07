package proxy

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const kiroMaxThinkingLength = 16000

const kiroAgenticSystemPrompt = `
# CRITICAL: CHUNKED WRITE PROTOCOL (MANDATORY)

You MUST follow these rules for ALL file operations. Violation causes server timeouts and task failure.

## ABSOLUTE LIMITS
- **MAXIMUM 350 LINES** per single write/edit operation - NO EXCEPTIONS
- **RECOMMENDED 300 LINES** or less for optimal performance
- **NEVER** write entire files in one operation if >300 lines

## MANDATORY CHUNKED WRITE STRATEGY

### For NEW FILES (>300 lines total):
1. FIRST: Write initial chunk (first 250-300 lines) using write_to_file/fsWrite
2. THEN: Append remaining content in 250-300 line chunks using file append operations
3. REPEAT: Continue appending until complete

### For EDITING EXISTING FILES:
1. Use surgical edits (apply_diff/targeted edits) - change ONLY what's needed
2. NEVER rewrite entire files - use incremental modifications
3. Split large refactors into multiple small, focused edits

REMEMBER: When in doubt, write LESS per operation. Multiple small operations > one large operation.`

var (
	embeddedToolCallPattern = regexp.MustCompile(`\[Called\s+(?:tool\s+)?([A-Za-z0-9_.-]+)\s+with\s+(?:args|input):?\s*`)
	trailingCommaPattern    = regexp.MustCompile(`,\s*([}\]])`)
)

const (
	agenticModelSuffix = "-agentic"
	chatOnlyModelSuffix = "-chat"
)

// ParseModelThinkingAndAgentic resolves model name, thinking suffix, -agentic, and -chat variants.
func ParseModelThinkingAndAgentic(model string, thinkingSuffix string) (string, bool, bool, bool) {
	actual, thinking := ParseModelAndThinking(model, thinkingSuffix)
	lower := strings.ToLower(actual)

	agentic := false
	if strings.HasSuffix(lower, agenticModelSuffix) {
		actual = actual[:len(actual)-len(agenticModelSuffix)]
		agentic = true
		lower = strings.ToLower(actual)
	}

	chatOnly := false
	if strings.HasSuffix(lower, chatOnlyModelSuffix) {
		actual = actual[:len(actual)-len(chatOnlyModelSuffix)]
		chatOnly = true
	}

	return actual, thinking, agentic, chatOnly
}

// ParseEmbeddedToolCalls extracts [Called tool_name with args: {...}] from assistant text.
func ParseEmbeddedToolCalls(text string, processedIDs map[string]bool) (string, []KiroToolUse) {
	if !strings.Contains(text, "[Called") {
		return text, nil
	}

	var toolUses []KiroToolUse
	cleanText := text

	matches := embeddedToolCallPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	for i := len(matches) - 1; i >= 0; i-- {
		matchStart := matches[i][0]
		toolNameStart := matches[i][2]
		toolNameEnd := matches[i][3]
		if toolNameStart < 0 || toolNameEnd < 0 {
			continue
		}
		toolName := text[toolNameStart:toolNameEnd]

		jsonStart := matches[i][1]
		if jsonStart >= len(text) {
			continue
		}
		for jsonStart < len(text) && (text[jsonStart] == ' ' || text[jsonStart] == '\t') {
			jsonStart++
		}
		if jsonStart >= len(text) || text[jsonStart] != '{' {
			continue
		}

		jsonEnd := findMatchingJSONBracket(text, jsonStart)
		if jsonEnd < 0 {
			continue
		}

		jsonStr := text[jsonStart : jsonEnd+1]
		closingBracket := jsonEnd + 1
		for closingBracket < len(text) && text[closingBracket] != ']' {
			closingBracket++
		}
		if closingBracket >= len(text) {
			continue
		}
		matchEnd := closingBracket + 1

		repairedJSON := RepairJSON(jsonStr)
		var inputMap map[string]interface{}
		if err := json.Unmarshal([]byte(repairedJSON), &inputMap); err != nil {
			continue
		}

		toolUseID := "toolu_" + uuid.New().String()[:12]
		dedupeKey := toolName + ":" + repairedJSON
		if processedIDs != nil {
			if processedIDs[dedupeKey] {
				if matchStart >= 0 && matchEnd <= len(cleanText) && matchStart <= matchEnd {
					cleanText = cleanText[:matchStart] + cleanText[matchEnd:]
				}
				continue
			}
			processedIDs[dedupeKey] = true
		}

		toolUses = append(toolUses, KiroToolUse{
			ToolUseID: toolUseID,
			Name:      toolName,
			Input:     inputMap,
		})

		if matchStart >= 0 && matchEnd <= len(cleanText) && matchStart <= matchEnd {
			cleanText = cleanText[:matchStart] + cleanText[matchEnd:]
		}
	}

	return cleanText, toolUses
}

func findMatchingJSONBracket(text string, startPos int) int {
	if startPos >= len(text) {
		return -1
	}
	openChar := text[startPos]
	var closeChar byte
	switch openChar {
	case '{':
		closeChar = '}'
	case '[':
		closeChar = ']'
	default:
		return -1
	}

	depth := 1
	inString := false
	escapeNext := false
	for i := startPos + 1; i < len(text); i++ {
		char := text[i]
		if escapeNext {
			escapeNext = false
			continue
		}
		if char == '\\' && inString {
			escapeNext = true
			continue
		}
		if char == '"' {
			inString = !inString
			continue
		}
		if !inString {
			switch char {
			case openChar:
				depth++
			case closeChar:
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

// RepairJSON fixes common malformed tool argument JSON from upstream.
func RepairJSON(jsonString string) string {
	if jsonString == "" {
		return "{}"
	}
	str := strings.TrimSpace(jsonString)
	if str == "" {
		return "{}"
	}

	var testParse interface{}
	if err := json.Unmarshal([]byte(str), &testParse); err == nil {
		return str
	}
	originalStr := str

	str = escapeNewlinesInJSONStrings(str)
	str = trailingCommaPattern.ReplaceAllString(str, "$1")

	braceCount := 0
	bracketCount := 0
	inString := false
	escape := false
	lastValidIndex := -1

	for i := 0; i < len(str); i++ {
		char := str[i]
		if escape {
			escape = false
			continue
		}
		if char == '\\' {
			escape = true
			continue
		}
		if char == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch char {
		case '{':
			braceCount++
		case '}':
			braceCount--
		case '[':
			bracketCount++
		case ']':
			bracketCount--
		}
		if braceCount >= 0 && bracketCount >= 0 {
			lastValidIndex = i
		}
	}

	if braceCount > 0 || bracketCount > 0 {
		if lastValidIndex > 0 && lastValidIndex < len(str)-1 {
			str = str[:lastValidIndex+1]
			braceCount, bracketCount = countJSONBrackets(str)
		}
		for braceCount > 0 {
			str += "}"
			braceCount--
		}
		for bracketCount > 0 {
			str += "]"
			bracketCount--
		}
	}

	if err := json.Unmarshal([]byte(str), &testParse); err != nil {
		return originalStr
	}
	return str
}

func countJSONBrackets(str string) (braceCount, bracketCount int) {
	inString := false
	escape := false
	for i := 0; i < len(str); i++ {
		char := str[i]
		if escape {
			escape = false
			continue
		}
		if char == '\\' {
			escape = true
			continue
		}
		if char == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch char {
		case '{':
			braceCount++
		case '}':
			braceCount--
		case '[':
			bracketCount++
		case ']':
			bracketCount--
		}
	}
	return braceCount, bracketCount
}

func escapeNewlinesInJSONStrings(raw string) string {
	var result strings.Builder
	result.Grow(len(raw) + 100)
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if escaped {
			result.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' && inString {
			result.WriteByte(c)
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			result.WriteByte(c)
			continue
		}
		if inString {
			switch c {
			case '\n':
				result.WriteString("\\n")
			case '\r':
				result.WriteString("\\r")
			case '\t':
				result.WriteString("\\t")
			default:
				result.WriteByte(c)
			}
		} else {
			result.WriteByte(c)
		}
	}
	return result.String()
}

func mergeEmbeddedToolUses(content string, existing []KiroToolUse) (string, []KiroToolUse) {
	processed := map[string]bool{}
	for _, tu := range existing {
		if b, err := json.Marshal(tu.Input); err == nil {
			processed[tu.Name+":"+string(b)] = true
		}
	}
	cleaned, embedded := SanitizeToolNarrationContent(content, processed)
	if len(embedded) == 0 {
		return cleaned, existing
	}
	return cleaned, deduplicateToolUses(append(existing, embedded...))
}

func parseToolInputMap(raw string) map[string]interface{} {
	repaired := RepairJSON(raw)
	if repaired == "" {
		return map[string]interface{}{}
	}
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(repaired), &input); err != nil || input == nil {
		return map[string]interface{}{}
	}
	return input
}
