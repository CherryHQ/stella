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

// goal.go is the agent-facing surface for durable, self-driven work. A goal is
// a root goal: async work that survives restarts and converges to an
// acceptance contract. Creating one with --activate (the default) hands the
// goal to the background dispatcher, which claims and runs it without further
// prompting — this is how an agent schedules and pursues work that outlives the
// current conversation. Like every other stella CLI command, it is a thin
// client over the goal HTTP API.

func goalCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "goal",
		Aliases:  []string{"task"},
		Usage:    "Create and track durable goals the dispatcher runs to acceptance in the background",
		Category: "Feature",
		Description: `A goal is durable, async work that survives restarts and is accepted, not
just finished. Create one to hand yourself a long-running objective: the
dispatcher claims it and drives it through a bounded rework loop until its
acceptance contract passes — no further prompting needed.

Use this for work that outlives a single conversation: long research,
multi-step builds, anything you want carried to completion on its own.`,
		Subcommands: []*ucli.Command{
			goalCreateCommand(),
			goalListCommand(),
			goalGetCommand(),
			goalCancelCommand(),
			goalPlanCommand(),
			goalRevisionsCommand(),
			goalApproveCommand(),
			goalRejectCommand(),
			goalSubmitReviewCommand(),
			goalChildrenCommand(),
			goalActivateCommand(),
		},
	}
}

func goalCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a goal and, by default, run it immediately in the background",
		Description: `Creates a goal and (unless --activate=false) activates it so the dispatcher
picks it up and executes it in the background. A leaf (default) is executed
directly; a composite is planned first — propose its children with
"stella goal plan", then "stella goal approve" to materialize and run them.

The background worker sees only the title and intent you provide — write a
clear, self-contained intent describing what "done" means.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "title", Usage: "Short title (required)", Required: true},
			&ucli.StringFlag{Name: "intent", Usage: `What "done" means — the acceptance target in prose`},
			&ucli.StringFlag{Name: "kind", Usage: "leaf (default, directly executed) or composite (planned first)", Value: "leaf"},
			&ucli.StringFlag{Name: "priority", Usage: "routine (default) or urgent"},
			&ucli.BoolFlag{Name: "activate", Usage: "Activate immediately for a direct background run", Value: true},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			activate := c.Bool("activate")
			body := apiclient.CreateGoalJSONRequestBody{
				Title:    c.String("title"),
				AgentId:  agentID,
				Activate: &activate,
			}
			if v := c.String("intent"); v != "" {
				body.Intent = &v
			}
			if v := c.String("kind"); v != "" {
				k := apitypes.CreateGoalRequestKind(v)
				body.Kind = &k
			}
			if v := c.String("priority"); v != "" {
				p := apitypes.CreateGoalRequestPriority(v)
				body.Priority = &p
			}

			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateGoal(c.Context, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Goal %s created (%s, %s).\n", cli.ShortID(d.Id), d.Title, d.Lifecycle)
			return o.Err()
		},
	}
}

func goalListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List your goals (active ones by default)",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "terminal", Usage: "Show terminal/history goals instead of active ones"},
			&ucli.StringFlag{Name: "q", Usage: "Case-insensitive substring match on title/intent"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			params := &apiclient.ListGoalsParams{AgentId: &agentID}
			if c.IsSet("terminal") {
				params.Terminal = apiclient.Ptr(c.Bool("terminal"))
			}
			if v := c.String("q"); v != "" {
				params.Q = &v
			}

			list, err := apiclient.Call[apiclient.GoalList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListGoals(c.Context, params)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Goals) == 0 {
				o.Println("No goals.")
				return o.Err()
			}
			o.Printf("%-10s  %-28s  %-10s  %-14s  %s\n", "ID", "TITLE", "KIND", "LIFECYCLE", "ACCEPTANCE")
			for _, d := range list.Goals {
				o.Printf("%-10s  %-28s  %-10s  %-14s  %s\n",
					cli.ShortID(d.Id), cli.Truncate(d.Title, 28), string(d.Kind), string(d.Lifecycle), string(d.AcceptanceState))
			}
			return o.Err()
		},
	}
}

func goalGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show a goal's current state",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal get <goal-id>")
			}
			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetGoal(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("ID:         %s\n", d.Id)
			o.Printf("Title:      %s\n", d.Title)
			o.Printf("Kind:       %s\n", string(d.Kind))
			o.Printf("Lifecycle:  %s\n", string(d.Lifecycle))
			o.Printf("Acceptance: %s\n", string(d.AcceptanceState))
			if d.Intent != nil && *d.Intent != "" {
				o.Printf("Intent:     %s\n", *d.Intent)
			}
			if d.AttemptCount != nil {
				o.Printf("Attempts:   %d\n", *d.AttemptCount)
			}
			return o.Err()
		},
	}
}

func goalCancelCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "cancel",
		Usage:     "Cancel a goal (cascades to its non-terminal subtree)",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Usage: "Optional cancellation reason"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal cancel <goal-id>")
			}
			var body apiclient.CancelGoalJSONRequestBody
			if v := c.String("reason"); v != "" {
				body.Reason = &v
			}
			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.CancelGoal(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Goal %s cancelled (%s).\n", cli.ShortID(d.Id), d.Lifecycle)
			return o.Err()
		},
	}
}

// goalPlanCommand authors a composite's decomposition: the agent proposes the
// child goals (and optional sibling edges) as a draft revision. This is how a
// composite is planned without the Web UI — write the plan, then approve it.
func goalPlanCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "plan",
		Usage:     "Propose a composite's children as a draft revision (from --file or stdin)",
		ArgsUsage: "<goal-id>",
		Description: `Stages a decomposition plan for a composite goal as a new draft revision.
Reads a DecompositionContent JSON document from --file (or stdin when omitted):

  {"children": [
     {"key": "step-1", "title": "...", "intent": "...", "kind": "leaf"},
     {"key": "step-2", "title": "...", "intent": "...", "kind": "leaf"}
   ],
   "edges": [{"downstream_key": "step-2", "upstream_key": "step-1"}]}

The revision lands in 'draft'. Approve it with "stella goal approve" (auto-accept
when review_policy=none) to materialize the children and let the dispatcher run
them. Each child key is the stable materialize anchor — reusing a key across
revisions edits that child rather than duplicating it.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "Path to the plan JSON; reads stdin when omitted"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal plan <goal-id> [--file plan.json]")
			}
			content, err := readDecomposition(c.String("file"))
			if err != nil {
				return err
			}
			rev, err := apiclient.Call[apiclient.Revision](func(api *apiclient.Client) (*http.Response, error) {
				return api.PutRevision(c.Context, id, content)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, rev)
			}
			o := cli.Stdout(c)
			n := 0
			if rev.Content != nil {
				n = len(rev.Content.Children)
			}
			o.Printf("Revision %s staged (rev %d, %s, %d children).\n", cli.ShortID(rev.Id), rev.RevisionNo, rev.Status, n)
			o.Printf("Approve with: stella goal approve %s %s\n", cli.ShortID(id), cli.ShortID(rev.Id))
			return o.Err()
		},
	}
}

// readDecomposition reads a DecompositionContent JSON document from path, or
// from stdin when path is empty or "-".
func readDecomposition(path string) (apiclient.DecompositionContent, error) {
	var raw []byte
	var err error
	if path != "" && path != "-" {
		raw, err = os.ReadFile(path)
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return apiclient.DecompositionContent{}, fmt.Errorf("read plan: %w", err)
	}
	var content apiclient.DecompositionContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return apiclient.DecompositionContent{}, fmt.Errorf("parse plan JSON: %w", err)
	}
	return content, nil
}

func goalRevisionsCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "revisions",
		Usage:     "List a composite's decomposition revisions",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal revisions <goal-id>")
			}
			list, err := apiclient.Call[apiclient.RevisionList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListRevisions(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Revisions) == 0 {
				o.Println("No revisions.")
				return o.Err()
			}
			o.Printf("%-10s  %-4s  %-12s  %-8s  %s\n", "ID", "NO", "STATUS", "CHILDREN", "REVIEW")
			for _, r := range list.Revisions {
				n := 0
				if r.Content != nil {
					n = len(r.Content.Children)
				}
				o.Printf("%-10s  %-4d  %-12s  %-8d  %s\n", cli.ShortID(r.Id), r.RevisionNo, string(r.Status), n, string(r.ReviewPolicy))
			}
			return o.Err()
		},
	}
}

// goalApproveCommand accepts a revision and materializes its children. It routes
// by the revision's status: a 'draft' (review_policy=none) auto-accepts; an
// 'in_review' revision takes the human-approval path.
func goalApproveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "approve",
		Usage:     "Accept a revision and materialize its children",
		ArgsUsage: "<goal-id> <revision-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Usage: "Optional approval note (in_review revisions)"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id, revID := c.Args().Get(0), c.Args().Get(1)
			if id == "" || revID == "" {
				return fmt.Errorf("usage: stella goal approve <goal-id> <revision-id>")
			}
			rev, err := findRevision(c, id, revID)
			if err != nil {
				return err
			}
			var out apiclient.Revision
			switch rev.Status {
			case apitypes.Draft:
				out, err = apiclient.Call[apiclient.Revision](func(api *apiclient.Client) (*http.Response, error) {
					return api.AcceptRevision(c.Context, id, rev.Id)
				})
			case apitypes.InReview:
				var body apiclient.ApproveRevisionJSONRequestBody
				if v := c.String("reason"); v != "" {
					body.Reason = &v
				}
				out, err = apiclient.Call[apiclient.Revision](func(api *apiclient.Client) (*http.Response, error) {
					return api.ApproveRevision(c.Context, id, rev.Id, body)
				})
			default:
				return fmt.Errorf("revision %s is %s; only draft or in_review can be approved", cli.ShortID(rev.Id), rev.Status)
			}
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, out)
			}
			o := cli.Stdout(c)
			o.Printf("Revision %s %s and materialized.\n", cli.ShortID(out.Id), out.Status)
			o.Printf("See children: stella goal children %s\n", cli.ShortID(id))
			return o.Err()
		},
	}
}

func goalRejectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "reject",
		Usage:     "Reject an in_review revision (the composite stays active for rework)",
		ArgsUsage: "<goal-id> <revision-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Usage: "Optional rejection reason"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id, revID := c.Args().Get(0), c.Args().Get(1)
			if id == "" || revID == "" {
				return fmt.Errorf("usage: stella goal reject <goal-id> <revision-id>")
			}
			rev, err := findRevision(c, id, revID)
			if err != nil {
				return err
			}
			var body apiclient.RejectRevisionJSONRequestBody
			if v := c.String("reason"); v != "" {
				body.Reason = &v
			}
			out, err := apiclient.Call[apiclient.Revision](func(api *apiclient.Client) (*http.Response, error) {
				return api.RejectRevision(c.Context, id, rev.Id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, out)
			}
			o := cli.Stdout(c)
			o.Printf("Revision %s rejected (%s).\n", cli.ShortID(out.Id), out.Status)
			return o.Err()
		},
	}
}

func goalSubmitReviewCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "submit-review",
		Usage:     "Send a draft revision into human review (in_review)",
		ArgsUsage: "<goal-id> <revision-id>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id, revID := c.Args().Get(0), c.Args().Get(1)
			if id == "" || revID == "" {
				return fmt.Errorf("usage: stella goal submit-review <goal-id> <revision-id>")
			}
			rev, err := findRevision(c, id, revID)
			if err != nil {
				return err
			}
			out, err := apiclient.Call[apiclient.Revision](func(api *apiclient.Client) (*http.Response, error) {
				return api.SubmitRevisionReview(c.Context, id, rev.Id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, out)
			}
			o := cli.Stdout(c)
			o.Printf("Revision %s submitted for review (%s).\n", cli.ShortID(out.Id), out.Status)
			return o.Err()
		},
	}
}

func goalChildrenCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "children",
		Usage:     "List a composite's child goals",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal children <goal-id>")
			}
			list, err := apiclient.Call[apiclient.GoalList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListGoalChildren(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Goals) == 0 {
				o.Println("No children.")
				return o.Err()
			}
			o.Printf("%-10s  %-28s  %-10s  %-14s  %s\n", "ID", "TITLE", "KIND", "LIFECYCLE", "ACCEPTANCE")
			for _, d := range list.Goals {
				o.Printf("%-10s  %-28s  %-10s  %-14s  %s\n",
					cli.ShortID(d.Id), cli.Truncate(d.Title, 28), string(d.Kind), string(d.Lifecycle), string(d.AcceptanceState))
			}
			return o.Err()
		},
	}
}

// goalActivateCommand hands a draft goal to the dispatcher. For a composite it
// also flips its materialized children draft→ready, so this is the step that
// starts a planned composite running after "approve".
func goalActivateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "activate",
		Usage:     "Activate a draft goal so the dispatcher runs it (composite: also its children)",
		ArgsUsage: "<goal-id>",
		Description: `Moves a draft goal to ready so the background dispatcher claims it. A composite
must be planned and approved first (its plan gate blocks activation until a
revision is materialized); activating a composite cascades to its draft children
so the dispatcher can begin claiming the leaves under it.`,
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal activate <goal-id>")
			}
			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.ActivateGoal(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Goal %s activated (%s).\n", cli.ShortID(d.Id), d.Lifecycle)
			return o.Err()
		},
	}
}

// findRevision resolves a revision by full or short id within a goal's revision
// list, so commands accept the short ids the CLI prints. Defining the lookup
// here keeps the API surface (no single-revision GET) out of every caller.
func findRevision(c *ucli.Context, goalID, revID string) (apiclient.Revision, error) {
	list, err := apiclient.Call[apiclient.RevisionList](func(api *apiclient.Client) (*http.Response, error) {
		return api.ListRevisions(c.Context, goalID)
	})
	if err != nil {
		return apiclient.Revision{}, err
	}
	for _, r := range list.Revisions {
		if r.Id == revID || cli.ShortID(r.Id) == revID {
			return r, nil
		}
	}
	return apiclient.Revision{}, fmt.Errorf("revision %q not found on goal %s", revID, cli.ShortID(goalID))
}
