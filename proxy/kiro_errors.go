package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// KiroAPIError captures a non-2xx response from a Kiro upstream endpoint.
// ClientMessage() returns the upstream body/message for debugging agent loops.
type KiroAPIError struct {
	StatusCode int
	Endpoint   string
	Body       []byte
	Message    string
}

func (e *KiroAPIError) Error() string {
	if e == nil {
		return ""
	}
	if msg := e.ClientMessage(); msg != "" {
		return msg
	}
	return fmt.Sprintf("HTTP %d from %s", e.StatusCode, e.Endpoint)
}

// ClientMessage returns the best human-readable message extracted from Kiro's
// response body, falling back to the raw body text.
func (e *KiroAPIError) ClientMessage() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	body := strings.TrimSpace(string(e.Body))
	if body != "" {
		return body
	}
	return fmt.Sprintf("HTTP %d from %s", e.StatusCode, e.Endpoint)
}

func newKiroAPIError(statusCode int, endpoint string, body []byte) *KiroAPIError {
	return &KiroAPIError{
		StatusCode: statusCode,
		Endpoint:   endpoint,
		Body:       bytes.TrimSpace(body),
		Message:    extractKiroErrorMessage(body),
	}
}

func extractKiroErrorMessage(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return string(body)
	}

	var parts []string
	appendPart := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, existing := range parts {
			if existing == s {
				return
			}
		}
		parts = append(parts, s)
	}

	for _, key := range []string{"message", "Message", "errorMessage", "error_message"} {
		if v, ok := parsed[key].(string); ok {
			appendPart(v)
		}
	}
	for _, key := range []string{"reason", "Reason", "errorCode", "error_code", "__type"} {
		if v, ok := parsed[key].(string); ok {
			appendPart(v)
		}
	}

	if errObj, ok := parsed["error"].(map[string]interface{}); ok {
		if v, ok := errObj["message"].(string); ok {
			appendPart(v)
		}
		if v, ok := errObj["type"].(string); ok {
			appendPart(v)
		}
	}

	if len(parts) > 0 {
		return strings.Join(parts, " — ")
	}

	// Preserve the full upstream JSON/text when no known fields are present.
	return string(body)
}

func asKiroAPIError(err error) (*KiroAPIError, bool) {
	var kiroErr *KiroAPIError
	if errors.As(err, &kiroErr) {
		return kiroErr, true
	}
	return nil, false
}

// upstreamClientError maps a Kiro/upstream failure to client-facing HTTP status,
// error type, and message. When the error came from Kiro, the upstream message
// is returned verbatim to the caller.
func upstreamClientError(err error, api string) (status int, errType string, message string) {
	if err == nil {
		return 500, defaultAPIErrorType(api), "unknown error"
	}

	if kiroErr, ok := asKiroAPIError(err); ok {
		return mapUpstreamStatus(kiroErr.StatusCode, api), mapUpstreamErrorType(kiroErr.StatusCode, api), kiroErr.ClientMessage()
	}

	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "upstream request failed"
	}
	return 500, defaultAPIErrorType(api), msg
}

func defaultAPIErrorType(api string) string {
	switch api {
	case "openai", "responses":
		return "server_error"
	default:
		return "api_error"
	}
}

func mapUpstreamStatus(upstreamStatus int, api string) int {
	switch upstreamStatus {
	case 400, 401, 402, 403, 404, 408, 413, 422, 429:
		return upstreamStatus
	case 502, 503, 504:
		return upstreamStatus
	default:
		if upstreamStatus >= 400 && upstreamStatus < 500 {
			return upstreamStatus
		}
		if api == "openai" || api == "responses" {
			return 502
		}
		return 500
	}
}

func mapUpstreamErrorType(upstreamStatus int, api string) string {
	switch upstreamStatus {
	case 400:
		if api == "openai" || api == "responses" {
			return "invalid_request_error"
		}
		return "invalid_request_error"
	case 401:
		if api == "openai" || api == "responses" {
			return "authentication_error"
		}
		return "authentication_error"
	case 403:
		if api == "openai" || api == "responses" {
			return "permission_error"
		}
		return "permission_error"
	case 404:
		return "not_found_error"
	case 408:
		return "timeout_error"
	case 413:
		return "invalid_request_error"
	case 422:
		return "invalid_request_error"
	case 429:
		if api == "openai" || api == "responses" {
			return "rate_limit_exceeded"
		}
		return "rate_limit_error"
	case 402:
		return "payment_required"
	default:
		return defaultAPIErrorType(api)
	}
}
