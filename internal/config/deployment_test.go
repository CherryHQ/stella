package config

import "testing"

func TestRequireExternalDB(t *testing.T) {
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
				t.Setenv("STELLA_REQUIRE_EXTERNAL_DB", tc.value)
			} else {
				t.Setenv("STELLA_REQUIRE_EXTERNAL_DB", "")
			}
			got, err := RequireExternalDB()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("RequireExternalDB() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("RequireExternalDB() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("RequireExternalDB() = %v, want %v", got, tc.want)
			}
		})
	}
}
