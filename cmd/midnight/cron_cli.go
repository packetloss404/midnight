package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type cliCronOccurrence struct {
	ID             string     `json:"id"`
	DeploymentID   string     `json:"deployment_id"`
	DueAt          time.Time  `json:"due_at"`
	State          string     `json:"state"`
	Attempt        int        `json:"attempt"`
	MaxAttempts    int        `json:"max_attempts"`
	RetryAt        *time.Time `json:"retry_at,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	FailureCode    string     `json:"failure_code,omitempty"`
	FailureMessage string     `json:"failure_message,omitempty"`
}

// cronFlags are shared by `cron list` and `cron get`; both scope the same
// occurrence collection.
type cronFlags struct {
	serviceID     *string
	environmentID *string
	state         *string
	limit         *int
}

func defineCronFlags(fs *flag.FlagSet) cronFlags {
	return cronFlags{
		serviceID:     cliServiceIDFlag(fs, "service id"),
		environmentID: cliEnvironmentIDFlag(fs, "environment id"),
		state:         fs.String("state", "", "pending|running|retry_wait|succeeded|failed"),
		limit:         cliLimitFlag(fs, 50, "maximum rows (1-200)"),
	}
}

func runCron(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "list" && args[0] != "get") {
		fmt.Fprintln(stderr, "usage: midnight cron list|get --service-id ID --environment-id ID [--state STATE] [--limit N] [occurrence-id]")
		return 64
	}
	fs, cfg := newAPIFlagSet("midnight cron "+args[0], stderr)
	f := defineCronFlags(fs)
	serviceID, environmentID, state, limit := f.serviceID, f.environmentID, f.state, f.limit
	if err := fs.Parse(args[1:]); err != nil {
		return 64
	}
	if strings.TrimSpace(*serviceID) == "" || strings.TrimSpace(*environmentID) == "" || *limit < 1 || *limit > 200 || (args[0] == "get" && fs.NArg() != 1) {
		fmt.Fprintln(stderr, "midnight cron: service/environment and valid arguments are required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight cron: %s\n", err)
		return 64
	}
	base := "/api/v1alpha/services/" + url.PathEscape(*serviceID) + "/environments/" + url.PathEscape(*environmentID) + "/cron-occurrences"
	if args[0] == "get" {
		base += "/" + url.PathEscape(fs.Arg(0))
		var item cliCronOccurrence
		raw, err := client.doJSON(http.MethodGet, base, nil, "", &item)
		if err != nil {
			fmt.Fprintf(stderr, "midnight cron get: %s\n", err)
			return 1
		}
		return renderCLISingle(stdout, stderr, cfg.JSON, raw, item,
			func(value cliCronOccurrence) string { return value.ID },
			printCronOccurrence,
		)
	}
	q := url.Values{"limit": []string{fmt.Sprintf("%d", *limit)}}
	if strings.TrimSpace(*state) != "" {
		q.Set("state", *state)
	}
	var items []cliCronOccurrence
	raw, err := client.doJSON(http.MethodGet, base+"?"+q.Encode(), nil, "", &items)
	if err != nil {
		fmt.Fprintf(stderr, "midnight cron list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, items,
		func(value cliCronOccurrence) string { return value.ID },
		func(output io.Writer, rows []cliCronOccurrence) {
			for _, item := range rows {
				printCronOccurrence(output, item)
			}
		},
	)
}

func printCronOccurrence(w io.Writer, item cliCronOccurrence) {
	retry := ""
	if item.RetryAt != nil {
		retry = " retry_at=" + item.RetryAt.UTC().Format(time.RFC3339)
	}
	exit := ""
	if item.ExitCode != nil {
		exit = fmt.Sprintf(" exit=%d", *item.ExitCode)
	}
	failure := ""
	if item.FailureCode != "" {
		failure = " " + item.FailureCode + ": " + item.FailureMessage
	}
	fmt.Fprintf(w, "%s\t%s\tdue=%s\tattempt=%d/%d%s%s%s\n", item.ID, item.State, item.DueAt.UTC().Format(time.RFC3339), item.Attempt, item.MaxAttempts, retry, exit, failure)
}
