package proxy

import (
	"errors"
	"testing"
)

func TestExtractKiroErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "message field",
			body: `{"message":"Model not available"}`,
			want: "Model not available",
		},
		{
			name: "nested error object",
			body: `{"error":{"type":"invalid_request","message":"Bad input"}}`,
			want: "Bad input — invalid_request",
		},
		{
			name: "reason and message",
			body: `{"message":"Quota exceeded","reason":"ThrottlingException"}`,
			want: "Quota exceeded — ThrottlingException",
		},
		{
			name: "plain text body",
			body: "upstream unavailable",
			want: "upstream unavailable",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractKiroErrorMessage([]byte(tt.body))
			if got != tt.want {
				t.Fatalf("extractKiroErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUpstreamClientError(t *testing.T) {
	kiroErr := newKiroAPIError(429, "Kiro IDE", []byte(`{"message":"Too many requests"}`))

	status, errType, msg := upstreamClientError(kiroErr, "openai")
	if status != 429 {
		t.Fatalf("status = %d, want 429", status)
	}
	if errType != "rate_limit_exceeded" {
		t.Fatalf("errType = %q, want rate_limit_exceeded", errType)
	}
	if msg != "Too many requests" {
		t.Fatalf("msg = %q, want Too many requests", msg)
	}

	claudeStatus, claudeType, claudeMsg := upstreamClientError(kiroErr, "claude")
	if claudeStatus != 429 || claudeType != "rate_limit_error" || claudeMsg != "Too many requests" {
		t.Fatalf("claude mapping = (%d, %q, %q)", claudeStatus, claudeType, claudeMsg)
	}

	genericErr := errors.New("connection reset")
	status, errType, msg = upstreamClientError(genericErr, "openai")
	if status != 500 || errType != "server_error" || msg != "connection reset" {
		t.Fatalf("generic mapping = (%d, %q, %q)", status, errType, msg)
	}
}

func TestKiroAPIErrorClientMessage(t *testing.T) {
	err := newKiroAPIError(403, "CodeWhisperer", []byte(`{"message":"Access denied"}`))
	if err.Error() != "Access denied" {
		t.Fatalf("Error() = %q, want Access denied", err.Error())
	}
	if err.ClientMessage() != "Access denied" {
		t.Fatalf("ClientMessage() = %q, want Access denied", err.ClientMessage())
	}
}

func TestIsQuotaAndAuthError(t *testing.T) {
	if !isQuotaError(newKiroAPIError(429, "Kiro IDE", nil)) {
		t.Fatal("expected quota error for 429 KiroAPIError")
	}
	if !isAuthError(newKiroAPIError(401, "Kiro IDE", nil)) {
		t.Fatal("expected auth error for 401 KiroAPIError")
	}
	if isAuthError(errors.New("random failure")) {
		t.Fatal("did not expect auth error for generic error")
	}
}
