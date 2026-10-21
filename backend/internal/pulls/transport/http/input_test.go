package pullshttp

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/pulls/app"
)

func TestParseCommentInput(t *testing.T) {
	reply := int64(5)
	type issue = [3]string // code, path, message
	tests := []struct {
		name       string
		body       string
		want       app.CommentRequest
		wantIssues []issue
	}{
		{name: "minimal", body: `{"path":"a.ts","line":3,"body":"x"}`, want: app.CommentRequest{Path: "a.ts", Line: 3, Body: "x"}},
		{name: "full", body: `{"path":"a.ts","line":3,"side":"LEFT","body":"x","in_reply_to":5,"extra":1}`,
			want: app.CommentRequest{Path: "a.ts", Line: 3, Side: "LEFT", Body: "x", InReplyTo: &reply}},
		{name: "integral float", body: `{"path":"a","line":3.0,"body":"x"}`, want: app.CommentRequest{Path: "a", Line: 3, Body: "x"}},
		{name: "empty path", body: `{"path":"","line":1,"body":"x"}`, wantIssues: []issue{{"too_small", "path", "String must contain at least 1 character(s)"}}},
		{name: "line zero", body: `{"path":"a","line":0,"body":"x"}`, wantIssues: []issue{{"too_small", "line", "Number must be greater than 0"}}},
		{name: "line negative", body: `{"path":"a","line":-2,"body":"x"}`, wantIssues: []issue{{"too_small", "line", "Number must be greater than 0"}}},
		{name: "line fraction", body: `{"path":"a","line":1.5,"body":"x"}`, wantIssues: []issue{{"invalid_type", "line", "Expected integer, received float"}}},
		{name: "line string", body: `{"path":"a","line":"1","body":"x"}`, wantIssues: []issue{{"invalid_type", "line", "Expected number, received string"}}},
		{name: "missing body", body: `{"path":"a","line":1}`, wantIssues: []issue{{"invalid_type", "body", "Required"}}},
		{name: "bad side", body: `{"path":"a","line":1,"body":"x","side":"MIDDLE"}`, wantIssues: []issue{{"invalid_enum_value", "side", `Invalid enum value. Expected 'LEFT' | 'RIGHT', received "MIDDLE"`}}},
		{name: "null side", body: `{"path":"a","line":1,"body":"x","side":null}`, wantIssues: []issue{{"invalid_type", "side", "Expected 'LEFT' | 'RIGHT', received null"}}},
		{name: "bad reply", body: `{"path":"a","line":1,"body":"x","in_reply_to":"5"}`, wantIssues: []issue{{"invalid_type", "in_reply_to", "Expected number, received string"}}},
		{name: "all invalid", body: `{}`, wantIssues: []issue{
			{"invalid_type", "path", "Required"}, {"invalid_type", "line", "Required"}, {"invalid_type", "body", "Required"}}},
		{name: "no body", body: ``, wantIssues: []issue{{"invalid_type", "", "Required"}}},
		{name: "array", body: `[1]`, wantIssues: []issue{{"invalid_type", "", "Expected object, received array"}}},
		{name: "null", body: `null`, wantIssues: []issue{{"invalid_type", "", "Expected object, received null"}}},
		{name: "plain text", body: `hello`, wantIssues: []issue{{"invalid_type", "", "Expected object, received string"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, issues := parseCommentInput([]byte(tc.body))
			var gotIssues []issue
			for _, is := range issues {
				path := ""
				if len(is.Path) > 0 {
					path = is.Path[0].(string)
				}
				gotIssues = append(gotIssues, issue{is.Code, path, is.Message})
			}
			if diff := cmp.Diff(tc.wantIssues, gotIssues); diff != "" {
				t.Errorf("parseCommentInput(%s) issues mismatch (-want +got):\n%s", tc.body, diff)
			}
			if len(tc.wantIssues) == 0 {
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("parseCommentInput(%s) mismatch (-want +got):\n%s", tc.body, diff)
				}
			}
		})
	}
}
