package testbed

import "testing"

func TestConfiguredPortReadsTheEnvironment(t *testing.T) {
	// The mise task execs `testbed start` with no arguments, so this variable
	// is the only way to move the port without editing the task.
	for _, tc := range []struct {
		name string
		env  string
		want int
		bad  bool
	}{
		{name: "unset keeps the default", env: "", want: 25777},
		{name: "a free port is honoured", env: "25679", want: 25679},
		{name: "a non-number is refused", env: "http://127.0.0.1:25679", bad: true},
		{name: "out of range is refused", env: "70000", bad: true},
		{name: "zero is refused", env: "0", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STELLA_TESTBED_PORT", tc.env)
			got, err := configuredPort()
			if tc.bad {
				// A silently wrong port would start a server nobody polls.
				if err == nil {
					t.Fatalf("configuredPort() = %d, want an error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("configuredPort() = %d, %v; want %d, nil", got, err, tc.want)
			}
		})
	}
}
