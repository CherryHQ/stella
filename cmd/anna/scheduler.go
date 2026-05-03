package main

import (
	"fmt"

	ucli "github.com/urfave/cli/v2"
	schedulerclient "github.com/vaayne/anna/pkg/scheduler/client"
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

func schedulerAPI() (*schedulerclient.Client, error) {
	return schedulerclient.NewFromEnv()
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

			job, err := api.CreateJob(c.Context, schedulerclient.CreateJobRequest{
				Name:        c.String("name"),
				Message:     c.String("message"),
				Cron:        cron,
				Every:       every,
				At:          at,
				SessionMode: c.String("session-mode"),
				Enabled:     true,
				AgentID:     c.String("agent-id"),
			})
			if err != nil {
				return wrapServerErr(err)
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
			jobs, err := api.ListJobs(c.Context)
			if err != nil {
				return wrapServerErr(err)
			}
			if c.Bool("json") {
				return printJSON(jobs)
			}
			if len(jobs) == 0 {
				fmt.Println("No scheduled jobs.")
				return nil
			}
			fmt.Printf("%-10s  %-20s  %-20s  %-8s  %s\n", "ID", "NAME", "SCHEDULE", "MODE", "LAST RUN")
			for _, j := range jobs {
				sched := j.Cron
				if sched == "" {
					sched = j.Every
				}
				if sched == "" {
					sched = j.At
				}
				lastRun := j.LastRunAt
				if lastRun == "" {
					lastRun = "never"
				}
				fmt.Printf("%-10s  %-20s  %-20s  %-8s  %s\n",
					shortID(j.ID), truncate(j.Name, 20), truncate(sched, 20), j.SessionMode, lastRun)
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
			if err := api.DeleteJob(c.Context, id); err != nil {
				return wrapServerErr(err)
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
