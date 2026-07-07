package proxy

import (
	"encoding/json"
	"strconv"
	"strings"

	"kiro-go/logger"
)

type TruncationInfo struct {
	IsTruncated    bool
	TruncationType string
	ToolName       string
	ToolUseID      string
	RawInput       string
	ParsedFields   map[string]string
	ErrorMessage   string
}

const (
	TruncationTypeNone             = ""
	TruncationTypeEmptyInput       = "empty_input"
	TruncationTypeInvalidJSON      = "invalid_json"
	TruncationTypeMissingFields    = "missing_fields"
	TruncationTypeIncompleteString = "incomplete_string"
)

var knownWriteTools = map[string]bool{
	"Write":              true,
	"write_to_file":      true,
	"fsWrite":            true,
	"create_file":        true,
	"edit_file":          true,
	"apply_diff":         true,
	"str_replace_editor": true,
	"insert":             true,
}

var requiredFieldsByTool = map[string][]string{
	"Write":              {"file_path", "content"},
	"write_to_file":      {"path", "content"},
	"fsWrite":            {"path", "content"},
	"create_file":        {"path", "content"},
	"edit_file":          {"path"},
	"apply_diff":         {"path", "diff"},
	"str_replace_editor": {"path", "old_str", "new_str"},
	"Bash":               {"command", "cmd"},
	"execute":            {"command", "cmd"},
	"run_command":        {"command", "cmd"},
}

func DetectTruncation(toolName, toolUseID, rawInput string, parsedInput map[string]interface{}) TruncationInfo {
	info := TruncationInfo{
		ToolName:     toolName,
		ToolUseID:    toolUseID,
		RawInput:     rawInput,
		ParsedFields: make(map[string]string),
	}

	if strings.TrimSpace(rawInput) == "" {
		if _, hasRequirements := requiredFieldsByTool[toolName]; hasRequirements {
			info.IsTruncated = true
			info.TruncationType = TruncationTypeEmptyInput
			info.ErrorMessage = "Tool input was completely empty - API response may have been truncated before tool parameters were transmitted"
			logger.Warnf("[Truncation] %s for tool %s (ID: %s): empty input buffer",
				info.TruncationType, toolName, toolUseID)
			return info
		}
		return info
	}

	if len(parsedInput) == 0 {
		if looksLikeTruncatedJSON(rawInput) {
			info.IsTruncated = true
			info.TruncationType = TruncationTypeInvalidJSON
			info.ParsedFields = extractPartialFields(rawInput)
			info.ErrorMessage = buildTruncationErrorMessage(toolName, info.TruncationType, info.ParsedFields, rawInput)
			logger.Warnf("[Truncation] %s for tool %s (ID: %s): JSON parse failed, raw length=%d bytes",
				info.TruncationType, toolName, toolUseID, len(rawInput))
			return info
		}
	}

	if parsedInput != nil {
		requiredFields, hasRequirements := requiredFieldsByTool[toolName]
		if hasRequirements {
			missingFields := findMissingRequiredFields(parsedInput, requiredFields)
			if len(missingFields) > 0 {
				info.IsTruncated = true
				info.TruncationType = TruncationTypeMissingFields
				info.ParsedFields = extractParsedFieldNames(parsedInput)
				info.ErrorMessage = buildMissingFieldsErrorMessage(toolName, missingFields, info.ParsedFields)
				logger.Warnf("[Truncation] %s for tool %s (ID: %s): missing required fields: %v",
					info.TruncationType, toolName, toolUseID, missingFields)
				return info
			}
		}

		if knownWriteTools[toolName] {
			if contentTruncation := detectContentTruncation(parsedInput, rawInput); contentTruncation != "" {
				info.IsTruncated = true
				info.TruncationType = TruncationTypeIncompleteString
				info.ParsedFields = extractParsedFieldNames(parsedInput)
				info.ErrorMessage = contentTruncation
				logger.Warnf("[Truncation] %s for tool %s (ID: %s): %s",
					info.TruncationType, toolName, toolUseID, contentTruncation)
				return info
			}
		}
	}

	info.IsTruncated = false
	info.TruncationType = TruncationTypeNone
	return info
}

func looksLikeTruncatedJSON(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return false
	}

	openBraces := strings.Count(trimmed, "{")
	closeBraces := strings.Count(trimmed, "}")
	openBrackets := strings.Count(trimmed, "[")
	closeBrackets := strings.Count(trimmed, "]")

	if openBraces > closeBraces || openBrackets > closeBrackets {
		return true
	}

	lastChar := trimmed[len(trimmed)-1]
	if lastChar != '}' && lastChar != ']' {
		if lastChar == '"' || lastChar == ':' || lastChar == ',' {
			return true
		}
	}

	inString := false
	escaped := false
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
		}
	}
	return inString
}

func extractPartialFields(raw string) map[string]string {
	fields := make(map[string]string)
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return fields
	}

	content := strings.TrimPrefix(trimmed, "{")
	parts := strings.Split(content, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if colonIdx := strings.Index(part, ":"); colonIdx > 0 {
			key := strings.Trim(strings.TrimSpace(part[:colonIdx]), `"'`)
			value := strings.Trim(strings.TrimSpace(part[colonIdx+1:]), `"'`)
			if len(value) > 50 {
				value = value[:50] + "..."
			}
			fields[key] = value
		}
	}
	return fields
}

func extractParsedFieldNames(parsed map[string]interface{}) map[string]string {
	fields := make(map[string]string)
	for key, val := range parsed {
		switch v := val.(type) {
		case string:
			if len(v) > 50 {
				fields[key] = v[:50] + "..."
			} else {
				fields[key] = v
			}
		case nil:
			fields[key] = "<null>"
		default:
			fields[key] = "<present>"
		}
	}
	return fields
}

func findMissingRequiredFields(parsed map[string]interface{}, required []string) []string {
	var missing []string
	for _, field := range required {
		if _, exists := parsed[field]; !exists {
			missing = append(missing, field)
		}
	}
	if len(required) == 2 &&
		((required[0] == "command" && required[1] == "cmd") ||
			(required[0] == "cmd" && required[1] == "command")) &&
		len(missing) == 1 {
		return nil
	}
	return missing
}

func detectContentTruncation(parsed map[string]interface{}, rawInput string) string {
	content, hasContent := parsed["content"]
	if !hasContent {
		return ""
	}

	contentStr, isString := content.(string)
	if !isString {
		return ""
	}

	if len(rawInput) > 1000 && len(contentStr) < 100 {
		return "content field appears suspiciously short compared to raw input size"
	}

	if strings.Contains(contentStr, "```") {
		openFences := strings.Count(contentStr, "```")
		if openFences%2 != 0 {
			return "content contains unclosed code fence (```) suggesting truncation"
		}
	}

	return ""
}

func buildTruncationErrorMessage(toolName, truncationType string, parsedFields map[string]string, rawInput string) string {
	var sb strings.Builder
	sb.WriteString("Tool input was truncated by the API. ")

	switch truncationType {
	case TruncationTypeEmptyInput:
		sb.WriteString("No input data was received.")
	case TruncationTypeInvalidJSON:
		sb.WriteString("JSON was cut off mid-transmission. ")
		if len(parsedFields) > 0 {
			sb.WriteString("Partial fields received: ")
			first := true
			for k := range parsedFields {
				if !first {
					sb.WriteString(", ")
				}
				sb.WriteString(k)
				first = false
			}
		}
	case TruncationTypeMissingFields:
		sb.WriteString("Required fields are missing from the input.")
	case TruncationTypeIncompleteString:
		sb.WriteString("Content appears to be shortened or incomplete.")
	}

	sb.WriteString(" Received ")
	sb.WriteString(strconv.Itoa(len(rawInput)))
	sb.WriteString(" bytes. Please retry with smaller content chunks.")

	return sb.String()
}

func buildMissingFieldsErrorMessage(toolName string, missingFields []string, parsedFields map[string]string) string {
	var sb strings.Builder
	sb.WriteString("Tool '")
	sb.WriteString(toolName)
	sb.WriteString("' is missing required fields: ")
	sb.WriteString(strings.Join(missingFields, ", "))
	sb.WriteString(". Fields received: ")

	first := true
	for k := range parsedFields {
		if !first {
			sb.WriteString(", ")
		}
		sb.WriteString(k)
		first = false
	}

	sb.WriteString(". This usually indicates the API response was truncated.")
	return sb.String()
}

func GetTruncationSummary(info TruncationInfo) string {
	if !info.IsTruncated {
		return ""
	}
	result, _ := json.Marshal(map[string]interface{}{
		"tool":           info.ToolName,
		"type":           info.TruncationType,
		"parsed_fields":  info.ParsedFields,
		"raw_input_size": len(info.RawInput),
	})
	return string(result)
}

// ToolUseInputForClient returns tool input for downstream clients. Truncated
// tool calls are marked with SOFT_LIMIT_REACHED so agents can retry in smaller
// chunks instead of failing silently (cliproxy++ pattern).
func ToolUseInputForClient(tu KiroToolUse) map[string]interface{} {
	if tu.Input == nil {
		tu.Input = map[string]interface{}{}
	}
	if !tu.IsTruncated || tu.TruncationInfo == nil {
		return tu.Input
	}
	out := make(map[string]interface{}, len(tu.Input)+2)
	for k, v := range tu.Input {
		out[k] = v
	}
	out["_status"] = "SOFT_LIMIT_REACHED"
	if tu.TruncationInfo.TruncationType != "" {
		out["_truncation_type"] = tu.TruncationInfo.TruncationType
	}
	return out
}

func MarshalToolUseArguments(tu KiroToolUse) string {
	data, err := json.Marshal(ToolUseInputForClient(tu))
	if err != nil {
		return "{}"
	}
	return string(data)
}
