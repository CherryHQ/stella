package mcpconfig

import "testing"

func TestAuthenticationAndTimeout(t *testing.T) {
	for _, method := range []string{"none", "client_secret_basic", "client_secret_post"} {
		d, err := Parse([]byte(`{"url":"https://example.com/mcp","transport":"streamable_http","auth_type":"oauth","token_endpoint_auth_method":"` + method + `","call_timeout_seconds":300}`))
		if err != nil || d.TokenEndpointAuthMethod != method || d.CallTimeoutSeconds != 300 {
			t.Fatalf("method %s: declaration=%+v error=%v", method, d, err)
		}
	}
	for _, fields := range []string{
		`"auth_type":"bearer","token_endpoint_auth_method":"none"`,
		`"auth_type":"oauth","token_endpoint_auth_method":"unsupported"`,
		`"call_timeout_seconds":-1`,
		`"call_timeout_seconds":301`,
	} {
		if _, err := Parse([]byte(`{"url":"https://example.com/mcp","transport":"streamable_http",` + fields + `}`)); err == nil {
			t.Fatalf("accepted invalid declaration: %s", fields)
		}
	}
}

func TestOptionsStrictAndDescription(t *testing.T) {
	options, err := ParseOptions([]byte(`{"description":"public MCP","call_timeout_seconds":30}`))
	if err != nil || options.Description != "public MCP" || options.CallTimeoutSeconds != 30 {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	declaration, err := Parse([]byte(`{"url":"https://example.com/mcp","transport":"streamable_http","description":"public MCP","call_timeout_seconds":30}`))
	if err != nil || declaration.Description != "public MCP" || declaration.CallTimeoutSeconds != 30 {
		t.Fatalf("declaration=%+v err=%v", declaration, err)
	}
	if options, err := ParseOptions([]byte(`{"description":"line\nfeed"}`)); err != nil || options.Description != "line\nfeed" {
		t.Fatalf("multiline description=%+v err=%v", options, err)
	}
	for _, raw := range []string{
		`{"unknown":true}`,
		`{"description":null}`,
		`{"call_timeout_seconds":301}`,
	} {
		if _, err := ParseOptions([]byte(raw)); err == nil {
			t.Fatalf("ParseOptions(%s) accepted invalid input", raw)
		}
	}
}
