package pgbundle

import "testing"

func TestLinuxRuntimeSourceFromOSRelease(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
		ok   bool
	}{
		{
			name: "debian trixie",
			data: "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nVERSION_CODENAME=trixie\n",
			want: "trixie",
			ok:   true,
		},
		{
			name: "ubuntu jammy fallback",
			data: "NAME=Ubuntu\nUBUNTU_CODENAME=jammy\n",
			want: "jammy",
			ok:   true,
		},
		{
			name: "ubuntu noble fallback",
			data: "NAME=Ubuntu\nUBUNTU_CODENAME=noble\n",
			want: "noble",
			ok:   true,
		},
		{
			name: "ubuntu resolute fallback",
			data: "NAME=Ubuntu\nUBUNTU_CODENAME=resolute\n",
			want: "resolute",
			ok:   true,
		},
		{
			name: "unsupported",
			data: "VERSION_CODENAME=forky\n",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := linuxRuntimeSourceFromOSRelease(tt.data)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("linuxRuntimeSourceFromOSRelease() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
