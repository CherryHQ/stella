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
			cfg:  Config{ContainerPathPrefix: "/container/anna", HostPathPrefix: "/host/anna"},
			in:   "/container/anna",
			want: "/host/anna",
		},
		{
			name: "subpath rewrites to host prefix",
			cfg:  Config{ContainerPathPrefix: "/container/anna", HostPathPrefix: "/host/anna"},
			in:   "/container/anna/work/file.txt",
			want: "/host/anna/work/file.txt",
		},
		{
			name: "unrelated path unchanged",
			cfg:  Config{ContainerPathPrefix: "/container/anna", HostPathPrefix: "/host/anna"},
			in:   "/other/path",
			want: "/other/path",
		},
		{
			name: "prefix-like but not a directory boundary unchanged",
			cfg:  Config{ContainerPathPrefix: "/container/anna", HostPathPrefix: "/host/anna"},
			in:   "/container/annax/file",
			want: "/container/annax/file",
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
