package version

import "testing"

func TestIsDev(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{version: "", want: true},
		{version: "dev", want: true},
		{version: "v0.1.0-5-gabcdef-dirty", want: true},
		{version: "v0.1.0-5-gabcdef", want: true},
		{version: "0.1.0-dev", want: true},
		{version: "v0.1.0-rc.1", want: false},
		{version: "v0.1.0-beta.2", want: false},
		{version: "abcdef1", want: true},
		{version: "v0.1.0", want: false},
		{version: "0.1.0", want: false},
		{version: "v1.2.3", want: false},
		{version: "1.2.3", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			original := Version
			t.Cleanup(func() { Version = original })
			Version = tc.version

			if got := IsDev(); got != tc.want {
				t.Errorf("IsDev() for Version=%q = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}
