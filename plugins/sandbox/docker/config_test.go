package docker

import "testing"

func TestTranslateToDaemonPath(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		in   string
		want string
	}{
		{
			name: "no prefixes returns input unchanged",
			cfg:  Config{},
			in:   "/foo/bar",
			want: "/foo/bar",
		},
		{
			name: "exact prefix match swaps whole path",
			cfg:  Config{ContainerPathPrefix: "/container/stella", HostPathPrefix: "/host/stella"},
			in:   "/container/stella",
			want: "/host/stella",
		},
		{
			name: "subpath rewrites to host prefix",
			cfg:  Config{ContainerPathPrefix: "/container/stella", HostPathPrefix: "/host/stella"},
			in:   "/container/stella/work/file.txt",
			want: "/host/stella/work/file.txt",
		},
		{
			name: "unrelated path unchanged",
			cfg:  Config{ContainerPathPrefix: "/container/stella", HostPathPrefix: "/host/stella"},
			in:   "/other/path",
			want: "/other/path",
		},
		{
			name: "prefix-like but not a directory boundary unchanged",
			cfg:  Config{ContainerPathPrefix: "/container/stella", HostPathPrefix: "/host/stella"},
			in:   "/container/stellax/file",
			want: "/container/stellax/file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.TranslateToDaemonPath(tc.in)
			if got != tc.want {
				t.Errorf("TranslateToDaemonPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
