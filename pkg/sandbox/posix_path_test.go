package sandbox

import "testing"

func TestPOSIXPathRelative(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		target string
		want   string
		ok     bool
	}{
		{name: "exact", root: "/opt/stella", target: "/opt/stella", want: ".", ok: true},
		{name: "descendant", root: "/opt/stella", target: "/opt/stella/bin/tool", want: "bin/tool", ok: true},
		{name: "root", root: "/", target: "/tmp/work", want: "tmp/work", ok: true},
		{name: "windows separators normalize", root: `\opt\stella`, target: `\opt\stella\bin`, want: "bin", ok: true},
		{name: "cleaned descendant", root: "/opt/stella/./", target: "/opt/stella/cache/../bin", want: "bin", ok: true},
		{name: "sibling prefix", root: "/opt/stella", target: "/opt/stellad/bin", ok: false},
		{name: "traversal", root: "/opt/stella", target: "/opt/stella/../secret", ok: false},
		{name: "empty root", root: "", target: "/opt/stella", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := POSIXPathRelative(tt.root, tt.target)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("POSIXPathRelative(%q, %q) = %q, %v; want %q, %v", tt.root, tt.target, got, ok, tt.want, tt.ok)
			}
		})
	}
}
