package mcp

import (
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rowshape/rowshape/internal/toolerror"
)

// TestMCPErrorsCarryTheToolErrorContract is the agent-surface parity guard.
//
// Every MCP failure used to return a bare string. internal/exitcode exists
// specifically so "the tool could not run" is never confused with "the migration
// is unsafe", and internal/toolerror carries the categories an agent branches on
// to decide what to do next — retry the environment, fix the fixture, install a
// runner. An agent on MCP could make none of those distinctions, on the one
// surface built for agents.
func TestMCPErrorsCarryTheToolErrorContract(t *testing.T) {
	res := errorText(toolerror.FixtureParse, "reading fixture x.yaml failed", "check the fixture path")
	if !res.IsError {
		t.Error("IsError must stay set for clients that only check the flag")
	}
	if len(res.Content) == 0 {
		t.Fatal("no content returned")
	}
	body := contentText(t, res)

	var got toolerror.ToolError
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("the error payload must be the structured contract, got %q: %v", body, err)
	}
	if got.Rowshape != toolerror.Contract {
		t.Errorf("contract tag = %q, want %q", got.Rowshape, toolerror.Contract)
	}
	if got.Error_ != toolerror.Kind {
		t.Errorf("error marker = %q, want %q — this is what lets a consumer tell a tool error from a verdict", got.Error_, toolerror.Kind)
	}
	if got.Category != toolerror.FixtureParse {
		t.Errorf("category = %q, want %q", got.Category, toolerror.FixtureParse)
	}
	if got.Hint == "" {
		t.Error("the hint is what makes a tool error actionable; it must survive")
	}
}

// The categories must be the ones an agent can actually act on, not all the same
// bucket. A single generic category would be no better than the bare string it
// replaced.
func TestMCPErrorCategoriesAreDistinct(t *testing.T) {
	cases := []struct {
		name string
		cat  toolerror.Category
	}{
		{"fixture could not be read", toolerror.FixtureParse},
		{"caller passed something wrong", toolerror.BadUsage},
		{"target unreachable", toolerror.ConnectFailed},
	}
	seen := map[toolerror.Category]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := contentText(t, errorText(tc.cat, "msg", "hint"))
			var got toolerror.ToolError
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("payload not structured: %v", err)
			}
			if got.Category != tc.cat {
				t.Errorf("category = %q, want %q", got.Category, tc.cat)
			}
			seen[got.Category] = true
		})
	}
	if len(seen) != len(cases) {
		t.Errorf("categories collapsed to %d distinct values, want %d", len(seen), len(cases))
	}
}

// A tool error must never be mistakable for a verdict: the payload carries no
// "verdict" key, and carries the "error" marker instead.
func TestMCPErrorIsNotMistakableForAVerdict(t *testing.T) {
	body := contentText(t, errorText(toolerror.BadUsage, "no target given", "pass a URL"))
	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if _, hasVerdict := raw["verdict"]; hasVerdict {
		t.Error("a tool error must not carry a verdict field")
	}
	if raw["error"] != toolerror.Kind {
		t.Errorf(`payload["error"] = %v, want %q`, raw["error"], toolerror.Kind)
	}
}

// The version reported in the handshake must come from the build, not a
// hardcoded constant that drifts from the binary answering the call.
func TestServerVersionIsSettable(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "9.9.9-test"
	if NewServer() == nil {
		t.Fatal("NewServer returned nil")
	}
	if Version != "9.9.9-test" {
		t.Errorf("Version = %q, want the value the caller set", Version)
	}
	if old == "0.1.0" {
		t.Error("the default must not be the old hardcoded release version")
	}
}

// contentText pulls the text out of the first content block.
func contentText(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("result carried no content")
	}
	tc, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *sdk.TextContent", res.Content[0])
	}
	return tc.Text
}
