package main

import (
	"fmt"
	"time"

	ucli "github.com/urfave/cli/v2"
	apiclient "github.com/vaayne/anna/api/client"
)

func schedulerCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "scheduler",
		Usage: "Manage scheduled jobs",
		Subcommands: []*ucli.Command{
			schedulerAddCommand(),
			schedulerListCommand(),
			schedulerRemoveCommand(),
		},
	}
}

func schedulerAPI() (*apiclient.Client, error) {
	return newAPIClient()
}

func schedulerAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "add",
		Usage: "Create a scheduled job",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "name", Usage: "Job name (required)", Required: true},
			&ucli.StringFlag{Name: "message", Usage: "Prompt/instruction to run on schedule (required)", Required: true},
			&ucli.StringFlag{Name: "cron", Usage: "Cron expression, e.g. '0 9 * * 1-5' (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "every", Usage: "Go duration, e.g. '30m' or '2h' (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "at", Usage: "RFC3339 timestamp for a one-time job (use one of cron, every, or at)"},
			&ucli.StringFlag{Name: "session-mode", Usage: "Session behavior: reuse (default) or new", Value: "reuse"},
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID to run the job (defaults to default agent)"},
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

			api, err := schedulerAPI()
			if err != nil {
				return err
			}

			name := c.String("name")
			msg := c.String("message")
			mode := c.String("session-mode")
			agentID := c.String("agent-id")
			enabled := true
			body := apiclient.CreateSchedulerJobJSONRequestBody{
				Name:        &name,
				Message:     &msg,
				SessionMode: &mode,
				Enabled:     &enabled,
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
			if agentID != "" {
				body.AgentId = &agentID
			}

			resp, err := api.CreateSchedulerJob(c.Context, body)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var job apiclient.Job
			if err := decodeJSON(resp, &job); err != nil {
				return err
			}
			return printJSON(job)
		},
	}
}

func schedulerListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List scheduled jobs",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			api, err := schedulerAPI()
			if err != nil {
				return err
			}
			resp, err := api.ListSchedulerJobs(c.Context)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var list apiclient.JobList
			if err := decodeJSON(resp, &list); err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No scheduled jobs.")
				return nil
			}
			fmt.Printf("%-10s  %-20s  %-20s  %-8s  %s\n", "ID", "NAME", "SCHEDULE", "MODE", "LAST RUN")
			for _, j := range list.Items {
				sched := derefStr(j.Cron)
				if sched == "" {
					sched = derefStr(j.Every)
				}
				if sched == "" {
					sched = derefStr(j.At)
				}
				lastRun := "never"
				if j.LastRunAt != nil {
					lastRun = j.LastRunAt.Format(time.DateTime)
				}
				fmt.Printf("%-10s  %-20s  %-20s  %-8s  %s\n",
					shortID(j.Id), truncate(j.Name, 20), truncate(sched, 20), j.SessionMode, lastRun)
			}
			return nil
		},
	}
}

func schedulerRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Usage:     "Remove a scheduled job",
		ArgsUsage: "<job-id>",
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: anna scheduler remove <job-id>")
			}
			api, err := schedulerAPI()
			if err != nil {
				return err
			}
			resp, err := api.DeleteSchedulerJob(c.Context, id)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			if err := decodeJSON(resp, nil); err != nil {
				return err
			}
			fmt.Printf("Job %q removed.\n", id)
			return nil
		},
	}
}

// truncate shortens s to max chars, appending "…" when trimmed.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
