package cli

import (
	"slices"
	"testing"

	ucli "github.com/urfave/cli/v2"
)

func testApp() *ucli.App {
	return &ucli.App{
		Name: "stella",
		Commands: []*ucli.Command{
			{
				Name: "goal",
				Subcommands: []*ucli.Command{
					{
						Name:  "get",
						Flags: []ucli.Flag{&ucli.BoolFlag{Name: "json"}},
					},
					{
						Name: "create",
						Flags: []ucli.Flag{
							&ucli.StringFlag{Name: "title"},
							&ucli.BoolFlag{Name: "json"},
						},
					},
				},
			},
		},
	}
}

func TestHoistFlags(t *testing.T) {
	app := testApp()
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flag after positional is hoisted",
			in:   []string{"stella", "goal", "get", "abc", "--json"},
			want: []string{"stella", "goal", "get", "--json", "abc"},
		},
		{
			name: "already-leading flag is left in place",
			in:   []string{"stella", "goal", "get", "--json", "abc"},
			want: []string{"stella", "goal", "get", "--json", "abc"},
		},
		{
			name: "value flag keeps its value when hoisted",
			in:   []string{"stella", "goal", "create", "--title", "my goal", "--json"},
			want: []string{"stella", "goal", "create", "--title", "my goal", "--json"},
		},
		{
			name: "value flag whose value follows a positional",
			in:   []string{"stella", "goal", "create", "pos", "--title", "my goal"},
			want: []string{"stella", "goal", "create", "--title", "my goal", "pos"},
		},
		{
			name: "double dash stops reordering",
			in:   []string{"stella", "goal", "get", "--", "--json"},
			want: []string{"stella", "goal", "get", "--", "--json"},
		},
		{
			name: "inline value form is not split",
			in:   []string{"stella", "goal", "create", "pos", "--title=x"},
			want: []string{"stella", "goal", "create", "--title=x", "pos"},
		},
		{
			name: "no command path, nothing to do",
			in:   []string{"stella"},
			want: []string{"stella"},
		},
		{
			name: "unknown trailing flag stays a flag without eating next token",
			in:   []string{"stella", "goal", "get", "abc", "--bogus", "xyz"},
			want: []string{"stella", "goal", "get", "--bogus", "abc", "xyz"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := HoistFlags(app, tc.in)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("HoistFlags(%v)\n  got  %v\n  want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestHoistFlagsEndToEnd proves the reorder actually makes a flag-after-positional
// invocation parse, which the stdlib flag package otherwise drops.
func TestHoistFlagsEndToEnd(t *testing.T) {
	var gotJSON bool
	var gotID string
	app := &ucli.App{
		Name: "stella",
		Commands: []*ucli.Command{{
			Name: "goal",
			Subcommands: []*ucli.Command{{
				Name:  "get",
				Flags: []ucli.Flag{&ucli.BoolFlag{Name: "json"}},
				Action: func(c *ucli.Context) error {
					gotJSON = c.Bool("json")
					gotID = c.Args().First()
					return nil
				},
			}},
		}},
	}
	argv := []string{"stella", "goal", "get", "abc", "--json"}
	if err := app.Run(HoistFlags(app, argv)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !gotJSON {
		t.Errorf("json flag = false, want true after hoisting")
	}
	if gotID != "abc" {
		t.Errorf("arg = %q, want %q", gotID, "abc")
	}
}
