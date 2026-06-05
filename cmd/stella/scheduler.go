package main

import (
	"fmt"
	"net/http"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
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
			mode := c.String("session-mode")
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
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

func schedulerListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List scheduled jobs",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := taskAgentID(c)
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
			agentID, err := taskAgentID(c)
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
