package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
)

func isThinkingEnabledFromHeader(headers http.Header) bool {
	if headers == nil {
		return false
	}
	betaHeader := headers.Get("Anthropic-Beta")
	return betaHeader != "" && strings.Contains(betaHeader, "interleaved-thinking")
}

func isThinkingModeTagInBody(body []byte) bool {
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "<thinking_mode>") || !strings.Contains(bodyStr, "</thinking_mode>") {
		return false
	}
	startTag := "<thinking_mode>"
	endTag := "</thinking_mode>"
	startIdx := strings.Index(bodyStr, startTag)
	if startIdx < 0 {
		return false
	}
	startIdx += len(startTag)
	endIdx := strings.Index(bodyStr[startIdx:], endTag)
	if endIdx < 0 {
		return false
	}
	mode := bodyStr[startIdx : startIdx+endIdx]
	return mode == "interleaved" || mode == "enabled"
}

func enrichClaudeThinking(thinking bool, headers http.Header, body []byte, thinkingCfg *ClaudeThinkingConfig) bool {
	if thinking {
		return true
	}
	if isThinkingEnabledFromHeader(headers) {
		return true
	}
	if isClaudeThinkingRequested(thinkingCfg) {
		return true
	}
	if isThinkingModeTagInBody(body) {
		return true
	}
	return false
}

func enrichOpenAIThinking(thinking bool, headers http.Header, body []byte, req *OpenAIRequest) bool {
	if thinking {
		return true
	}
	if isThinkingEnabledFromHeader(headers) {
		return true
	}
	if isThinkingModeTagInBody(body) {
		return true
	}
	if req != nil {
		if effort := strings.ToLower(strings.TrimSpace(req.ReasoningEffort)); effort != "" && effort != "none" {
			return true
		}
	}
	var probe struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Model           string `json:"model"`
	}
	if len(body) > 0 && json.Unmarshal(body, &probe) == nil {
		if effort := strings.ToLower(strings.TrimSpace(probe.ReasoningEffort)); effort != "" && effort != "none" {
			return true
		}
		lower := strings.ToLower(probe.Model)
		if strings.Contains(lower, "thinking") || strings.Contains(lower, "-reason") {
			return true
		}
	}
	return false
}
