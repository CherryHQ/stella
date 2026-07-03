package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func schedulerCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "scheduler",
		Usage:    "Create cron, interval, or one-shot jobs that trigger the agent on a schedule",
		Category: "Feature",
		Description: `Schedule recurring or one-time agent jobs using cron expressions, Go
durations, or absolute timestamps. The agent runs the job's instruction
at the specified time and can optionally notify you with the results.`,
		Subcommands: []*ucli.Command{
			schedulerAddCommand(),
			schedulerListCommand(),
			schedulerRemoveCommand(),
			schedulerTemplatesCommand(),
			schedulerSubscribeCommand(),
			schedulerUnsubscribeCommand(),
		},
	}
}

func schedulerAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "add",
		Usage: "Create a scheduled job",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "name", Usage: "Job name (required)", Required: true},
			&ucli.StringFlag{Name: "message", Usage: "Prompt/instruction to run on schedule"},
			&ucli.StringFlag{Name: "workflow", Usage: "Workflow ID to instantiate instead of sending a chat message"},
			&ucli.StringSliceFlag{Name: "input", Usage: "Workflow input as k=v (repeatable)"},
			&ucli.BoolFlag{Name: "allow-replan", Usage: "Allow scheduling a partially frozen workflow"},
			&ucli.StringFlag{Name: "cron", Usage: "Cron expression, e.g. '0 9 * * 1-5' (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "every", Usage: "Go duration, e.g. '30m' or '2h' (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "at", Usage: "RFC3339 timestamp for a one-time job (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "session-mode", Usage: "Session behavior: reuse (default) or new", Value: "reuse"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			cron := c.String("cron")
			every := c.String("every")
			at := c.String("at")
			set := 0
			if cron != "" {
				set++
			}
			if every != "" {
				set++
			}
			if at != "" {
				set++
			}
			if set == 0 {
				return fmt.Errorf("one of --cron, --every, or --at is required")
			}
			if set > 1 {
				return fmt.Errorf("only one of --cron, --every, or --at may be set")
			}

			name := c.String("name")
			msg := c.String("message")
			workflowID := c.String("workflow")
			if workflowID != "" && msg != "" {
				return fmt.Errorf("--workflow and --message are mutually exclusive")
			}
			if workflowID == "" && msg == "" {
				return fmt.Errorf("--message is required unless --workflow is set")
			}
			inputs, err := parseInputFlags(c.StringSlice("input"))
			if err != nil {
				return err
			}
			if workflowID == "" && len(inputs) > 0 {
				return fmt.Errorf("--input requires --workflow")
			}
			mode := c.String("session-mode")
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			enabled := true
			body := apiclient.CreateSchedulerJobJSONRequestBody{
				Name:        &name,
				SessionMode: &mode,
				Enabled:     &enabled,
			}
			if workflowID != "" {
				kind := apitypes.JobInputDispatchKindWorkflow
				body.DispatchKind = &kind
				body.WorkflowId = &workflowID
				body.Inputs = &inputs
				allowReplan := c.Bool("allow-replan")
				if allowReplan {
					body.AllowReplan = &allowReplan
				}
			} else {
				kind := apitypes.JobInputDispatchKindChat
				body.DispatchKind = &kind
				body.Message = &msg
			}
			if cron != "" {
				body.Cron = &cron
			}
			if every != "" {
				body.Every = &every
			}
			if at != "" {
				body.At = &at
			}

			job, err := apiclient.Call[apiclient.Job](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateSchedulerJob(c.Context, agentID, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, job)
			}
			sched := cli.DerefStr(job.Cron)
			if sched == "" {
				sched = cli.DerefStr(job.Every)
			}
			if sched == "" {
				sched = cli.DerefStr(job.At)
			}
			o := cli.Stdout(c)
			o.Printf("Job %s created (%s, %s).\n", cli.ShortID(job.Id), job.Name, sched)
			return o.Err()
		},
	}
}

func parseInputFlags(values []string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --input %q: expected k=v", value)
		}
		out[key] = val
	}
	return out, nil
}

func schedulerListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List scheduled jobs",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			list, err := apiclient.Call[apiclient.JobList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListSchedulerJobs(c.Context, agentID)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Jobs) == 0 {
				o.Println("No scheduled jobs.")
				return o.Err()
			}
			o.Printf("%-10s  %-20s  %-20s  %-8s  %s\n", "ID", "NAME", "SCHEDULE", "MODE", "LAST RUN")
			for _, j := range list.Jobs {
				sched := cli.DerefStr(j.Cron)
				if sched == "" {
					sched = cli.DerefStr(j.Every)
				}
				if sched == "" {
					sched = cli.DerefStr(j.At)
				}
				lastRun := "never"
				if j.LastRunAt != nil {
					lastRun = j.LastRunAt.Format(time.DateTime)
				}
				o.Printf("%-10s  %-20s  %-20s  %-8s  %s\n",
					cli.ShortID(j.Id), cli.Truncate(j.Name, 20), cli.Truncate(sched, 20), j.SessionMode, lastRun)
			}
			return o.Err()
		},
	}
}

func schedulerRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Usage:     "Remove a scheduled job",
		ArgsUsage: "<job-id>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella scheduler remove <job-id>")
			}
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteSchedulerJob(c.Context, agentID, id)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, id)
			}
			o := cli.Stdout(c)
			o.Printf("Job %q removed.\n", id)
			return o.Err()
		},
	}
}

func schedulerTemplatesCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "templates",
		Usage: "List available job templates",
		Description: `Lists platform-provided job templates. Each template is a pre-configured
scheduled task you can subscribe to. The SUBSCRIBED column shows the short
job ID when you already have an active subscription, or "-" if not.

Template prompts are platform-managed and read-only. To manage subscriptions,
use 'stella scheduler subscribe --help' and 'stella scheduler unsubscribe --help'.`,
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apiclient.JobTemplateList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListJobTemplates(c.Context)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.JobTemplates) == 0 {
				o.Println("No job templates available.")
				return o.Err()
			}
			o.Printf("%-20s  %-25s  %-22s  %s\n", "KEY", "NAME", "DEFAULT SCHEDULE", "SUBSCRIBED")
			for _, t := range list.JobTemplates {
				sub := "-"
				if t.SubscribedJobId != nil && *t.SubscribedJobId != "" {
					sub = cli.ShortID(*t.SubscribedJobId)
				}
				o.Printf("%-20s  %-25s  %-22s  %s\n",
					cli.Truncate(t.Key, 20), cli.Truncate(t.Name, 25), cli.Truncate(t.DefaultSchedule, 22), sub)
			}
			return o.Err()
		},
	}
}

func schedulerSubscribeCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "subscribe",
		Usage:     "Subscribe to a job template",
		ArgsUsage: "<template-key>",
		Description: `Creates a scheduled job from a platform-provided template. The prompt is
managed by the platform and cannot be edited. One subscription per template
is allowed; a second subscribe returns an "already subscribed" message.

Use 'stella scheduler templates' to list available template keys.
Use 'stella scheduler unsubscribe <template-key>' to cancel a subscription.

Optional schedule override flags (--cron or --every) replace the template's
default schedule. Omit them to use the template default.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "cron", Usage: "Override default schedule with a cron expression, e.g. '0 9 * * 1-5'"},
			&ucli.StringFlag{Name: "every", Usage: "Override default schedule with a Go duration, e.g. '12h'"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			key := c.Args().First()
			if key == "" {
				return fmt.Errorf("usage: stella scheduler subscribe <template-key>")
			}
			cron := c.String("cron")
			every := c.String("every")
			if cron != "" && every != "" {
				return fmt.Errorf("only one of --cron or --every may be set")
			}

			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			enabled := true
			body := apiclient.CreateSchedulerJobJSONRequestBody{
				TemplateKey: apiclient.Ptr(key),
				Enabled:     &enabled,
			}
			if cron != "" {
				body.Cron = &cron
			}
			if every != "" {
				body.Every = &every
			}

			job, err := apiclient.Call[apiclient.Job](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateSchedulerJob(c.Context, agentID, body)
			})
			if err != nil {
				// 409 means this user is already subscribed; surface a friendly message.
				if strings.Contains(err.Error(), "stella server 409") {
					return fmt.Errorf("already subscribed to template %q; use 'stella scheduler templates' to see the job ID", key)
				}
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, job)
			}
			sched := cli.DerefStr(job.Cron)
			if sched == "" {
				sched = cli.DerefStr(job.Every)
			}
			o := cli.Stdout(c)
			o.Printf("Subscribed: job %s created (%s, %s).\n", cli.ShortID(job.Id), job.Name, sched)
			return o.Err()
		},
	}
}

func schedulerUnsubscribeCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "unsubscribe",
		Usage:     "Unsubscribe from a job template",
		ArgsUsage: "<template-key>",
		Description: `Removes your subscription to a platform-provided job template by deleting
the corresponding scheduled job. If you are not subscribed, a message is
printed and the command exits successfully.

Use 'stella scheduler templates' to see which templates you are subscribed to.`,
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			key := c.Args().First()
			if key == "" {
				return fmt.Errorf("usage: stella scheduler unsubscribe <template-key>")
			}

			// Look up the subscribed job ID from the templates list.
			list, err := apiclient.Call[apiclient.JobTemplateList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListJobTemplates(c.Context)
			})
			if err != nil {
				return err
			}
			var jobID, subAgentID string
			for _, t := range list.JobTemplates {
				if t.Key == key {
					if t.SubscribedJobId != nil {
						jobID = *t.SubscribedJobId
					}
					if t.SubscribedAgentId != nil {
						subAgentID = *t.SubscribedAgentId
					}
					break
				}
			}
			if jobID == "" {
				o := cli.Stdout(c)
				o.Printf("Not subscribed to template %q.\n", key)
				return o.Err()
			}

			// Use the agent that owns the subscription job; fall back to the
			// CLI's task agent when the server doesn't supply subscribed_agent_id
			// (old server version).
			deleteAgentID := subAgentID
			if deleteAgentID == "" {
				deleteAgentID, err = scopedAgentIDFromEnv()
				if err != nil {
					return err
				}
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteSchedulerJob(c.Context, deleteAgentID, jobID)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, jobID)
			}
			o := cli.Stdout(c)
			o.Printf("Unsubscribed from template %q (job %s removed).\n", key, cli.ShortID(jobID))
			return o.Err()
		},
	}
}
