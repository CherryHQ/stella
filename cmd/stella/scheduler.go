package main

import (
	"fmt"
	"net/http"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
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
		},
	}
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

			job, err := apiclient.CallJSON[apiclient.Job](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateSchedulerJob(c.Context, body)
			})
			if err != nil {
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
			list, err := apiclient.CallJSON[apiclient.JobList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListSchedulerJobs(c.Context)
			})
			if err != nil {
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
				return fmt.Errorf("usage: stella scheduler remove <job-id>")
			}
			if err := apiclient.DoJSON(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteSchedulerJob(c.Context, id)
			}); err != nil {
				return err
			}
			fmt.Printf("Job %q removed.\n", id)
			return nil
		},
	}
}

// truncate shortens s to max chars, appending "…" when trimmed.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
