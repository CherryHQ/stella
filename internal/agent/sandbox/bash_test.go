package sandbox

import "testing"

func TestStellaCLIVerbPath(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
		ok   bool
	}{
		{name: "goal create", cmd: " stella goal create --title secret --intent also-secret", want: "goal create", ok: true},
		{name: "scheduler list", cmd: "stella scheduler list --json", want: "scheduler list", ok: true},
		{name: "single verb", cmd: "stella version", want: "version", ok: true},
		{name: "not stella", cmd: "echo stella goal create", ok: false},
		{name: "prefix only", cmd: "stella", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stellaCLIVerbPath(tt.cmd)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("stellaCLIVerbPath(%q)=(%q,%v), want (%q,%v)", tt.cmd, got, ok, tt.want, tt.ok)
			}
		})
	}
}
