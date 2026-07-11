package config

import "testing"

func TestStrictDeployment(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		set     bool
		want    bool
		wantErr bool
	}{
		{name: "unset", set: false, want: false},
		{name: "empty", value: "", set: true, want: false},
		{name: "one", value: "1", set: true, want: true},
		{name: "true", value: "true", set: true, want: true},
		{name: "zero", value: "0", set: true, want: false},
		{name: "false", value: "false", set: true, want: false},
		{name: "garbage", value: "yes", set: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("STELLA_STRICT_DEPLOYMENT", tc.value)
			} else {
				t.Setenv("STELLA_STRICT_DEPLOYMENT", "")
			}
			got, err := StrictDeployment()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("StrictDeployment() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("StrictDeployment() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("StrictDeployment() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAllowUnsafeBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		set     bool
		want    bool
		wantErr bool
	}{
		{name: "unset", set: false, want: false},
		{name: "empty", value: "", set: true, want: false},
		{name: "one", value: "1", set: true, want: true},
		{name: "true", value: "true", set: true, want: true},
		{name: "zero", value: "0", set: true, want: false},
		{name: "false", value: "false", set: true, want: false},
		{name: "garbage", value: "nope", set: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("STELLA_ALLOW_UNSAFE_BASE_URL", tc.value)
			} else {
				t.Setenv("STELLA_ALLOW_UNSAFE_BASE_URL", "")
			}
			got, err := AllowUnsafeBaseURL()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("AllowUnsafeBaseURL() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("AllowUnsafeBaseURL() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("AllowUnsafeBaseURL() = %v, want %v", got, tc.want)
			}
		})
	}
}
