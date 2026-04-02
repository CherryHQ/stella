package pluginapi

import "testing"

func TestRPCError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *RPCError
		want string
	}{
		{"nil", nil, ""},
		{"empty", &RPCError{}, ""},
		{"code only", &RPCError{Code: "not_found"}, "not_found"},
		{"message only", &RPCError{Message: "gone"}, "gone"},
		{"both", &RPCError{Code: "bad_request", Message: "missing field"}, "bad_request: missing field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
