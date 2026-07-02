package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func goalHealthCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "health",
		Usage: "Show goal execution health metrics",
		Description: `Reports goal execution health for a time window: lifecycle distribution,
attempt success and failure classes, decomposition repair quality, budget burn
attribution, and p50/p95 latencies. The --since flag accepts a duration (14d,
336h) or an RFC3339 timestamp.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "since", Value: "14d", Usage: "Start of the report window as a duration or RFC3339 timestamp"},
			&ucli.StringFlag{Name: "user", Usage: "Filter to one user ID (admin only unless it is your own user)"},
			&ucli.StringFlag{Name: "agent", Usage: "Filter to one agent ID"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			since, err := parseGoalHealthSince(c.String("since"), time.Now().UTC())
			if err != nil {
				return err
			}
			params := &apiclient.GetGoalHealthParams{Since: since}
			if v := c.String("user"); v != "" {
				params.UserId = &v
			}
			if v := c.String("agent"); v != "" {
				params.AgentId = &v
			}
			report, err := apiclient.Call[apiclient.GoalHealthReport](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetGoalHealth(c.Context, params)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, report)
			}
			return printGoalHealth(c, report)
		},
	}
}

func parseGoalHealthSince(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "14d"
	}
	if before, ok := strings.CutSuffix(raw, "d"); ok {
		days, err := strconv.Atoi(before)
		if err != nil || days < 0 {
			return time.Time{}, fmt.Errorf("invalid --since %q", raw)
		}
		return now.Add(-time.Duration(days) * 24 * time.Hour).UTC(), nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("invalid --since %q", raw)
		}
		return now.Add(-d).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --since %q: use duration like 14d/336h or RFC3339", raw)
	}
	return t.UTC(), nil
}

func printGoalHealth(c *ucli.Context, r apitypes.GoalHealthReport) error {
	o := cli.Stdout(c)
	o.Printf("Goal execution health (%s -> %s)\n", r.SinceAt.UTC().Format(time.RFC3339), r.UntilAt.UTC().Format(time.RFC3339))
	if r.UserId != nil || r.AgentId != nil {
		o.Printf("Filters: user=%s agent=%s\n", ptrOrAll(r.UserId), ptrOrAll(r.AgentId))
	}
	o.Println("")

	o.Printf("Goals: %d\n", r.TotalGoals)
	printCountTable(o, "Lifecycle", r.LifecycleCounts)
	printCountTable(o, "Blocked reasons", r.BlockedReasonCounts)
	o.Println("")

	o.Println("Attempts by purpose")
	o.Printf("%-15s  %7s  %9s  %8s  %11s  %11s\n", "PURPOSE", "TOTAL", "SUCCEEDED", "SUCCESS", "AVG_RETRIES", "MAX_RETRIES")
	for _, p := range r.AttemptPurposes {
		o.Printf("%-15s  %7d  %9d  %7.1f%%  %11.2f  %11d\n", string(p.Purpose), p.Total, p.Succeeded, p.SuccessRate*100, p.AverageRetries, p.MaxRetries)
	}
	printCountTable(o, "Failure classes", r.FailureClassCounts)
	o.Printf("Acceptance events: total=%d pass=%d fail=%d pass_rate=%.1f%%\n", r.AcceptanceEvents.Total, r.AcceptanceEvents.Passed, r.AcceptanceEvents.Failed, r.AcceptanceEvents.PassRate*100)
	o.Println("")

	dq := r.DecompositionQuality
	o.Println("Decomposition quality")
	o.Printf("First-round success: %d/%d (%.1f%%)\n", dq.FirstRoundSucceeded, dq.FirstRoundTotal, dq.FirstRoundSuccessRate*100)
	o.Printf("Average repair rounds: %.2f\n", dq.AverageRepairRounds)
	printCountTable(o, "Redecompositions per goal", dq.RedecompositionCounts)
	o.Println("")

	ba := r.BudgetAttribution
	o.Println("Budget attribution")
	o.Printf("Model-budget attempts: %d\n", ba.ModelBudgetAttempts)
	o.Printf("%-15s  %7s  %8s\n", "CLASS", "COUNT", "RATIO")
	for _, b := range ba.ClassCounts {
		o.Printf("%-15s  %7d  %7.1f%%\n", b.Key, b.Count, b.Ratio*100)
	}
	o.Printf("Flaky-dominant blocked goals: %d\n", ba.FlakyDominantBlockedGoals)
	o.Println("")

	o.Println("Latency")
	o.Printf("%-15s  %10s  %10s\n", "PHASE", "P50", "P95")
	for _, l := range r.Latency.Attempts {
		o.Printf("%-15s  %10s  %10s\n", string(l.Purpose), ms(l.P50Ms), ms(l.P95Ms))
	}
	o.Printf("%-15s  %10s  %10s\n", "goal_e2e", ms(r.Latency.GoalE2e.P50Ms), ms(r.Latency.GoalE2e.P95Ms))
	return o.Err()
}

func printCountTable(o *cli.LineWriter, title string, rows []apitypes.GoalHealthCountBucket) {
	o.Println(title)
	if len(rows) == 0 {
		o.Println("  (none)")
		return
	}
	o.Printf("  %-24s  %7s\n", "KEY", "COUNT")
	for _, row := range rows {
		o.Printf("  %-24s  %7d\n", emptyKey(row.Key), row.Count)
	}
}

func ptrOrAll(v *string) string {
	if v == nil || *v == "" {
		return "all"
	}
	return *v
}

func emptyKey(v string) string {
	if v == "" {
		return "(empty)"
	}
	return v
}

func ms(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.0fms", *v)
}
