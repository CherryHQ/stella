package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

// workflow.go is the agent-facing surface for reusable, schedulable workflows.
// A workflow is a frozen goal: the decomposition plan of a once-accepted
// composite, captured so instantiating it skips the planner and deterministically
// rebuilds the subtree. The common path is "save-as" — freeze an accepted goal —
// then "instantiate" to run a fresh copy on demand. Like every stella CLI
// command, it is a thin client over the workflow HTTP API.

func workflowCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "workflow",
		Aliases:  []string{"wf"},
		Usage:    "Freeze accepted goals into reusable, schedulable workflows and run them on demand",
		Category: "Feature",
		Description: `A workflow is a frozen goal: the decomposition plan of an accepted composite,
captured so it can be re-run without planning again. Instantiating a workflow
skips the planner and deterministically rebuilds the goal subtree, then hands it
to the dispatcher like any other goal.

Typical flow: accept a composite goal, "stella workflow save-as <goal-id>" to
freeze it, then "stella workflow instantiate <workflow-id>" whenever you want a
fresh run.`,
		Subcommands: []*ucli.Command{
			workflowListCommand(),
			workflowGetCommand(),
			workflowSaveAsCommand(),
			workflowCreateCommand(),
			workflowInstantiateCommand(),
			workflowScheduleCommand(),
			workflowUpdateCommand(),
			workflowDeleteCommand(),
		},
	}
}

func workflowListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List your workflows",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "q", Usage: "Case-insensitive substring match on name/intent"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			params := &apiclient.ListWorkflowsParams{AgentId: &agentID}
			if v := c.String("q"); v != "" {
				params.Q = &v
			}
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
			o.Printf("%-10s  %-30s  %-8s  %s\n", "ID", "NAME", "VERSION", "INTENT")
			for _, wf := range list.Workflows {
				intent := ""
				if wf.Intent != nil {
					intent = cli.Truncate(*wf.Intent, 40)
				}
				o.Printf("%-10s  %-30s  v%-7d  %s\n",
					cli.ShortID(wf.Id), cli.Truncate(wf.Name, 30), wf.Version, intent)
			}
			return o.Err()
		},
	}
}

func workflowGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show a workflow, including its frozen plan",
		ArgsUsage: "<workflow-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow get <workflow-id>")
			}
			wf, err := apiclient.Call[apitypes.Workflow](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetWorkflow(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, wf)
			}
			o := cli.Stdout(c)
			o.Printf("ID:       %s\n", wf.Id)
			o.Printf("Name:     %s\n", wf.Name)
			o.Printf("Version:  %d\n", wf.Version)
			o.Printf("Owner:    %s\n", wf.OwnerKind)
			if wf.AgentId != nil {
				o.Printf("Agent:    %s\n", *wf.AgentId)
			}
			if wf.Intent != nil && *wf.Intent != "" {
				o.Printf("Intent:   %s\n", *wf.Intent)
			}
			if wf.SourceGoalId != nil {
				o.Printf("Source:   %s\n", *wf.SourceGoalId)
			}
			return o.Err()
		},
	}
}

// workflowSaveAsCommand freezes an accepted composite goal into a workflow. The
// source goal must be an accepted composite owned by the caller.
func workflowSaveAsCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "save-as",
		Usage:     "Freeze an accepted composite goal into a reusable workflow",
		ArgsUsage: "<goal-id>",
		Description: `Captures the decomposition plan of an accepted composite goal as a workflow.
The goal must be an accepted composite you own. The resulting workflow can be
instantiated to re-run the same plan without planning again.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "name", Usage: "Workflow name (defaults to the source goal's title)"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow save-as <goal-id>")
			}
			var body apiclient.SaveGoalAsWorkflowJSONRequestBody
			if v := c.String("name"); v != "" {
				body.Name = &v
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
			o.Printf("Workflow %s saved from goal %s (%s, v%d).\n", cli.ShortID(wf.Id), cli.ShortID(id), wf.Name, wf.Version)
			o.Printf("Instantiate it: stella workflow instantiate %s\n", cli.ShortID(wf.Id))
			return o.Err()
		},
	}
}

// workflowCreateCommand stores a hand-authored workflow from a frozen plan JSON.
// This is the rarer path; "save-as" is the usual way to mint a workflow.
func workflowCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a hand-authored workflow from a frozen plan",
		Description: `Stores a workflow from a hand-authored frozen plan. Most workflows come from
"stella workflow save-as <goal-id>" instead; use this only when you are
authoring a plan directly.

The plan, acceptance contract, and convergence policy are JSON; pass each
inline or from a file ("-" reads stdin).`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "name", Usage: "Workflow name (required)", Required: true},
			&ucli.StringFlag{Name: "intent", Usage: `What "done" means — the acceptance target in prose`},
			&ucli.StringFlag{Name: "plan", Usage: "Frozen plan JSON (inline)"},
			&ucli.StringFlag{Name: "plan-file", Usage: `Frozen plan JSON file ("-" for stdin)`},
			&ucli.StringFlag{Name: "contract", Usage: "Acceptance contract JSON (inline)"},
			&ucli.StringFlag{Name: "contract-file", Usage: `Acceptance contract JSON file ("-" for stdin)`},
			&ucli.StringFlag{Name: "policy", Usage: "Convergence policy JSON (inline)"},
			&ucli.StringFlag{Name: "policy-file", Usage: `Convergence policy JSON file ("-" for stdin)`},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			plan, err := jsonObjectArg(c, "plan", "plan-file")
			if err != nil {
				return err
			}
			if plan == nil {
				return fmt.Errorf("a frozen plan is required: pass --plan or --plan-file")
			}
			body := apiclient.CreateWorkflowJSONRequestBody{
				Name:    c.String("name"),
				AgentId: agentID,
				Plan:    *plan,
			}
			if v := c.String("intent"); v != "" {
				body.Intent = &v
			}
			if contract, err := jsonObjectArg(c, "contract", "contract-file"); err != nil {
				return err
			} else if contract != nil {
				body.AcceptanceContract = contract
			}
			if policy, err := jsonObjectArg(c, "policy", "policy-file"); err != nil {
				return err
			} else if policy != nil {
				body.ConvergencePolicy = policy
			}
			wf, err := apiclient.Call[apitypes.Workflow](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateWorkflow(c.Context, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, wf)
			}
			o := cli.Stdout(c)
			o.Printf("Workflow %s created (%s, v%d).\n", cli.ShortID(wf.Id), wf.Name, wf.Version)
			return o.Err()
		},
	}
}

// workflowInstantiateCommand materializes a workflow into a live goal tree and
// hands it to the dispatcher.
func workflowInstantiateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "instantiate",
		Aliases:   []string{"run"},
		Usage:     "Instantiate a workflow into a live goal tree the dispatcher runs",
		ArgsUsage: "<workflow-id>",
		Description: `Rebuilds the workflow's frozen plan into a fresh goal subtree, skipping the
planner, and activates it so the background dispatcher runs it to acceptance.
Returns the new root goal; track it with "stella goal get <goal-id>".`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "project", Usage: "Project to scope the instantiated goal tree to"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow instantiate <workflow-id>")
			}
			var body apiclient.InstantiateWorkflowJSONRequestBody
			if v := c.String("project"); v != "" {
				body.ProjectId = &v
			}
			d, err := apiclient.Call[apitypes.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.InstantiateWorkflow(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Workflow %s instantiated as goal %s (%s, %s).\n", cli.ShortID(id), cli.ShortID(d.Id), d.Title, d.Lifecycle)
			o.Printf("Track it: stella goal get %s\n", cli.ShortID(d.Id))
			return o.Err()
		},
	}
}

// workflowScheduleCommand schedules a workflow so the dispatcher instantiates a
// fresh goal tree on each fire. The managing agent is taken from the workflow.
func workflowScheduleCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "schedule",
		Usage:     "Schedule a workflow to instantiate on a recurring or one-time basis",
		ArgsUsage: "<workflow-id>",
		Description: `Creates a scheduler job that instantiates the workflow into a fresh goal tree
on each fire. Provide exactly one of --cron, --every, or --at. Each run is
handed to the background dispatcher like any other goal.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "name", Usage: "Scheduler job name (required)", Required: true},
			&ucli.StringFlag{Name: "cron", Usage: "Cron expression, e.g. '0 9 * * 1-5' (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "every", Usage: "Go duration, e.g. '12h' (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "at", Usage: "RFC3339 timestamp for a one-time run (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "project", Usage: "Project to scope each instantiated goal tree to"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow schedule <workflow-id>")
			}
			body := apiclient.ScheduleWorkflowJSONRequestBody{Name: c.String("name")}
			if v := c.String("cron"); v != "" {
				body.Cron = &v
			}
			if v := c.String("every"); v != "" {
				body.Every = &v
			}
			if v := c.String("at"); v != "" {
				body.At = &v
			}
			if v := c.String("project"); v != "" {
				body.ProjectId = &v
			}
			job, err := apiclient.Call[apitypes.Job](func(api *apiclient.Client) (*http.Response, error) {
				return api.ScheduleWorkflow(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, job)
			}
			o := cli.Stdout(c)
			o.Printf("Workflow %s scheduled as job %s (%s).\n", cli.ShortID(id), cli.ShortID(job.Id), job.Name)
			return o.Err()
		},
	}
}

func workflowUpdateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "update",
		Usage:     "Update a workflow's name or intent",
		ArgsUsage: "<workflow-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "name", Usage: "New name"},
			&ucli.StringFlag{Name: "intent", Usage: "New intent"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow update <workflow-id>")
			}
			if !c.IsSet("name") && !c.IsSet("intent") {
				return fmt.Errorf("nothing to update: pass --name and/or --intent")
			}
			var body apiclient.UpdateWorkflowJSONRequestBody
			if c.IsSet("name") {
				v := c.String("name")
				body.Name = &v
			}
			if c.IsSet("intent") {
				v := c.String("intent")
				body.Intent = &v
			}
			wf, err := apiclient.Call[apitypes.Workflow](func(api *apiclient.Client) (*http.Response, error) {
				return api.UpdateWorkflow(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, wf)
			}
			o := cli.Stdout(c)
			o.Printf("Workflow %s updated (%s).\n", cli.ShortID(wf.Id), wf.Name)
			return o.Err()
		},
	}
}

func workflowDeleteCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "delete",
		Usage:     "Delete a workflow",
		ArgsUsage: "<workflow-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella workflow delete <workflow-id>")
			}
			err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteWorkflow(c.Context, id)
			})
			if err != nil {
				return err
			}
			o := cli.Stdout(c)
			o.Printf("Workflow %s deleted.\n", cli.ShortID(id))
			return o.Err()
		},
	}
}

// jsonObjectArg reads a JSON object from an inline flag or a file flag ("-" reads
// stdin), returning nil when neither is set. The two flags are mutually
// exclusive.
func jsonObjectArg(c *ucli.Context, inlineFlag, fileFlag string) (*map[string]any, error) {
	inline := c.String(inlineFlag)
	file := c.String(fileFlag)
	if inline != "" && file != "" {
		return nil, fmt.Errorf("pass only one of --%s or --%s", inlineFlag, fileFlag)
	}
	var raw []byte
	switch {
	case inline != "":
		raw = []byte(inline)
	case file == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read --%s from stdin: %w", fileFlag, err)
		}
		raw = b
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read --%s: %w", fileFlag, err)
		}
		raw = b
	default:
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("invalid JSON for --%s: %w", inlineFlag, err)
	}
	return &m, nil
}
