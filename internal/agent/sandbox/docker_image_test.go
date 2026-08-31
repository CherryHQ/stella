package sandbox

import (
	"testing"

	"github.com/CherryHQ/stella/internal/version"
)

func TestDockerImage(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    string
	}{
		{name: "empty", version: "", want: "stella-sandbox:dev"},
		{name: "literal dev", version: "dev", want: "stella-sandbox:dev"},
		{name: "dirty describe", version: "v0.1.0-5-gabcdef-dirty", want: "stella-sandbox:dev"},
		{name: "describe with commits", version: "v0.1.0-5-gabcdef", want: "stella-sandbox:dev"},
		{name: "release candidate", version: "v0.1.0-rc.1", want: "ghcr.io/cherryhq/stella-sandbox:0.1.0-rc.1"},
		{name: "release with v prefix", version: "v0.1.0", want: "ghcr.io/cherryhq/stella-sandbox:0.1.0"},
		{name: "release without v prefix", version: "0.1.0", want: "ghcr.io/cherryhq/stella-sandbox:0.1.0"},
		{name: "release semver patch", version: "v1.2.3", want: "ghcr.io/cherryhq/stella-sandbox:1.2.3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := version.Version
			t.Cleanup(func() { version.Version = original })
			version.Version = tc.version

			if got := dockerImage(); got != tc.want {
				t.Errorf("dockerImage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDockerImageIsDev(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{version: "", want: true},
		{version: "dev", want: true},
		{version: "v0.1.0-5-gabcdef-dirty", want: true},
		{version: "v0.1.0-rc.1", want: false},
		{version: "v0.1.0", want: false},
		{version: "0.1.0", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			original := version.Version
			t.Cleanup(func() { version.Version = original })
			version.Version = tc.version

			if got := dockerImageIsDev(); got != tc.want {
				t.Errorf("dockerImageIsDev() for %q = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}
