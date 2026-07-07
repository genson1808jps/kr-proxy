package proxy

import "testing"

func TestToolUseInputForClientMarksTruncation(t *testing.T) {
	tu := KiroToolUse{
		ToolUseID: "toolu_1",
		Name:      "Write",
		Input:     map[string]interface{}{"file_path": "a.txt"},
		IsTruncated: true,
		TruncationInfo: &TruncationInfo{
			IsTruncated:    true,
			TruncationType: TruncationTypeMissingFields,
		},
	}
	got := ToolUseInputForClient(tu)
	if got["_status"] != "SOFT_LIMIT_REACHED" {
		t.Fatalf("expected SOFT_LIMIT_REACHED, got %#v", got)
	}
	if got["_truncation_type"] != TruncationTypeMissingFields {
		t.Fatalf("expected truncation type marker, got %#v", got)
	}
	if got["file_path"] != "a.txt" {
		t.Fatalf("expected partial input preserved, got %#v", got)
	}
}

func TestToolUseInputForClientUntruncatedPassthrough(t *testing.T) {
	tu := KiroToolUse{
		Name:  "Bash",
		Input: map[string]interface{}{"cmd": "ls"},
	}
	got := ToolUseInputForClient(tu)
	if _, ok := got["_status"]; ok {
		t.Fatalf("expected no soft-limit marker, got %#v", got)
	}
}
