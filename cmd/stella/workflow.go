package main

import (
	"fmt"
	"net/http"
	"strings"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func workflowCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "workflow",
		Usage:    "Save and run reusable workflows from accepted goals",
		Category: "Feature",
		Description: `A workflow is a named, versioned recipe frozen from an accepted composite goal.
Running it creates a fresh goal tree without replanning frozen nodes.

Use this when a successful goal should become reusable structure instead of
being reopened. Done goals stay done; workflow runs are new goals.`,
		Subcommands: []*ucli.Command{
			workflowSaveCommand(),
			workflowListCommand(),
			workflowShowCommand(),
			workflowRunCommand(),
		},
	}
}

func workflowSaveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "save",
		Usage:     "Save an accepted composite goal as a workflow",
		ArgsUsage: "<goal-id>",
		Description: `Freezes an accepted composite goal's decomposition plan as a new immutable
workflow version. Inputs are text placeholders referenced by the frozen plan as
{{inputs.name}}.

Examples:
  stella workflow save 8f3a --name "Weekly release notes"
  stella workflow save 8f3a --name "Brief" --input topic:required --input audience=team`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "name", Usage: "Workflow name (required)", Required: true},
			&ucli.StringSliceFlag{Name: "input", Usage: "Input spec as name[:required][=default]; repeatable"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow save <goal-id> --name <name>")
			}
			inputs, err := parseWorkflowInputSpecs(c.StringSlice("input"))
			if err != nil {
				return err
			}
			body := apiclient.SaveGoalAsWorkflowJSONRequestBody{Name: c.String("name")}
			if len(inputs) > 0 {
				body.Inputs = &inputs
			}
			wf, err := apiclient.Call[apitypes.Workflow](func(api *apiclient.Client) (*http.Response, error) {
				return api.SaveGoalAsWorkflow(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, wf)
			}
			o := cli.Stdout(c)
			o.Printf("Workflow %s saved (%s v%d).\n", cli.ShortID(wf.Id), wf.Name, wf.Version)
			return o.Err()
		},
	}
}

func workflowListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List workflows",
		Flags: []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			params := &apiclient.ListWorkflowsParams{AgentId: &agentID}
			list, err := apiclient.Call[apitypes.WorkflowList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListWorkflows(c.Context, params)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Workflows) == 0 {
				o.Println("No workflows.")
				return o.Err()
			}
			o.Printf("%-10s  %-28s  %-7s  %-6s  %s\n", "ID", "NAME", "VERSION", "FROZEN", "SOURCE")
			for _, wf := range list.Workflows {
				source := "-"
				if wf.SourceGoalId != nil {
					source = cli.ShortID(*wf.SourceGoalId)
				}
				o.Printf("%-10s  %-28s  %-7d  %-6t  %s\n", cli.ShortID(wf.Id), cli.Truncate(wf.Name, 28), wf.Version, wf.FullyFrozen, source)
			}
			return o.Err()
		},
	}
}

func workflowShowCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "show",
		Usage:     "Show workflow details and recent runs",
		ArgsUsage: "<workflow-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow show <workflow-id>")
			}
			wf, err := apiclient.Call[apitypes.Workflow](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetWorkflow(c.Context, id)
			})
			if err != nil {
				return err
			}
			limit := 10
			runs, err := apiclient.Call[apitypes.WorkflowRunList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListWorkflowRuns(c.Context, id, &apiclient.ListWorkflowRunsParams{PageSize: &limit})
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, map[string]any{"workflow": wf, "runs": runs.Runs})
			}
			o := cli.Stdout(c)
			o.Printf("ID:             %s\n", wf.Id)
			o.Printf("Name:           %s\n", wf.Name)
			o.Printf("Version:        %d\n", wf.Version)
			o.Printf("Fully frozen:   %t\n", wf.FullyFrozen)
			o.Printf("Payload format: %s\n", wf.PayloadFormat)
			if wf.SourceGoalId != nil {
				o.Printf("Source goal:    %s\n", *wf.SourceGoalId)
			}
			if len(wf.Inputs) > 0 {
				o.Println("Inputs:")
				for _, in := range wf.Inputs {
					required := false
					if in.Required != nil {
						required = *in.Required
					}
					o.Printf("  - %s required=%t default=%q\n", in.Name, required, cli.DerefStr(in.Default))
				}
			}
			if len(runs.Runs) > 0 {
				o.Println("Recent runs:")
				for _, run := range runs.Runs {
					root := "-"
					if run.RootGoalId != nil {
						root = cli.ShortID(*run.RootGoalId)
					}
					o.Printf("  %-10s  %-14s  root=%s\n", cli.ShortID(run.Id), run.Status, root)
				}
			}
			return o.Err()
		},
	}
}

func workflowRunCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "run",
		Usage:     "Instantiate a workflow as a fresh goal tree",
		ArgsUsage: "<workflow-id>",
		Description: `Runs a workflow by materializing a new goal tree. Provide --idempotency-key
for retries; without it the server generates a new key and each call creates a
new run.

Examples:
  stella workflow run 4b12 --input topic=security
  stella workflow run 4b12 --idempotency-key nightly-2026-07-03`,
		Flags: []ucli.Flag{
			&ucli.StringSliceFlag{Name: "input", Usage: "Input value as key=value; repeatable"},
			&ucli.StringFlag{Name: "idempotency-key", Usage: "Stable key for retry-safe instantiation"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow run <workflow-id>")
			}
			inputs, err := parseWorkflowInputValues(c.StringSlice("input"))
			if err != nil {
				return err
			}
			body := apiclient.InstantiateWorkflowJSONRequestBody{}
			if len(inputs) > 0 {
				body.Inputs = &inputs
			}
			if key := c.String("idempotency-key"); key != "" {
				body.IdempotencyKey = &key
			}
			run, err := apiclient.Call[apitypes.WorkflowRun](func(api *apiclient.Client) (*http.Response, error) {
				return api.InstantiateWorkflow(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, run)
			}
			root := "-"
			if run.RootGoalId != nil {
				root = cli.ShortID(*run.RootGoalId)
			}
			o := cli.Stdout(c)
			o.Printf("Workflow run %s %s (root goal %s).\n", cli.ShortID(run.Id), run.Status, root)
			return o.Err()
		},
	}
}

func parseWorkflowInputSpecs(values []string) ([]apitypes.WorkflowInputSpec, error) {
	out := make([]apitypes.WorkflowInputSpec, 0, len(values))
	for _, raw := range values {
		left, def, hasDefault := strings.Cut(raw, "=")
		name, marker, hasMarker := strings.Cut(left, ":")
		if name == "" {
			return nil, fmt.Errorf("invalid --input %q: name is required", raw)
		}
		required := hasMarker && marker == "required"
		if hasMarker && marker != "required" {
			return nil, fmt.Errorf("invalid --input %q: only :required is supported", raw)
		}
		item := apitypes.WorkflowInputSpec{Name: name}
		if required {
			item.Required = &required
		}
		if hasDefault {
			item.Default = &def
		}
		out = append(out, item)
	}
	return out, nil
}

func parseWorkflowInputValues(values []string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --input %q: use key=value", raw)
		}
		out[key] = value
	}
	return out, nil
}
