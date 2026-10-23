// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/midnight/midnight/pkg/midnightctl"
)

type usageFlags struct {
	workspaceID *string
	since       *string
	until       *string
}

func defineUsageFlags(fs *flag.FlagSet) usageFlags {
	return usageFlags{
		workspaceID: fs.String("workspace-id", "", "workspace id"),
		since:       fs.String("since", "", "inclusive RFC3339 window start"),
		until:       fs.String("until", "", "exclusive RFC3339 window end"),
	}
}

func runUsage(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight usage", stderr)
	f := defineUsageFlags(fs)
	workspaceID, sinceValue, untilValue := f.workspaceID, f.since, f.until
	if err := fs.Parse(args); err != nil {
		return cliUsageExitCode
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "midnight usage: unexpected argument %q\n", fs.Arg(0))
		return cliUsageExitCode
	}
	if strings.TrimSpace(*workspaceID) == "" {
		fmt.Fprintln(stderr, "midnight usage: --workspace-id is required")
		return cliUsageExitCode
	}
	since, err := parseOptionalRFC3339("--since", *sinceValue)
	if err != nil {
		fmt.Fprintf(stderr, "midnight usage: %s\n", err)
		return cliUsageExitCode
	}
	until, err := parseOptionalRFC3339("--until", *untilValue)
	if err != nil {
		fmt.Fprintf(stderr, "midnight usage: %s\n", err)
		return cliUsageExitCode
	}
	if since != nil && until != nil && !since.Before(*until) {
		fmt.Fprintln(stderr, "midnight usage: --since must be before --until")
		return cliUsageExitCode
	}

	client, err := newGeneratedCLIReadClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight usage: %s\n", err)
		return cliUsageExitCode
	}
	params := &midnightctl.GetUsageSummaryParams{
		WorkspaceId: strings.TrimSpace(*workspaceID),
		Since:       since,
		Until:       until,
	}
	var summary midnightctl.UsageSummary
	ctx, cancel := cliOutputContext(stdout)
	defer cancel()
	raw, err := executeGeneratedCLIRead(
		func() (*http.Response, error) { return client.client.GetUsageSummary(ctx, params) },
		http.MethodGet,
		"/api/v1alpha/usage/summary",
		&summary,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight usage: %s\n", err)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, raw, summary,
		func(value midnightctl.UsageSummary) string { return value.WorkspaceId },
		writeUsageSummary,
	)
}

func parseOptionalRFC3339(name, value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("%s must be RFC3339: %w", name, err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func writeUsageSummary(output io.Writer, summary midnightctl.UsageSummary) {
	fmt.Fprintf(output, "Workspace: %s\n", summary.WorkspaceId)
	fmt.Fprintf(output, "Window:    %s to %s\n", summary.WindowStart.UTC().Format(time.RFC3339), summary.WindowEnd.UTC().Format(time.RFC3339))
	fmt.Fprintf(output, "As of:     %s\n", summary.AsOf.UTC().Format(time.RFC3339))
	fmt.Fprintf(output, "Policy:    %s\n\n", summary.PolicyProfile)

	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "DIMENSION\tUSED\tLIMIT\tENFORCED\tCOVERAGE\tSOURCE")
	for _, line := range summary.Usage {
		limit := "-"
		if line.Limit != nil {
			limit = fmt.Sprintf("%d", *line.Limit)
		}
		fmt.Fprintf(writer, "%s\t%d %s\t%s\t%t\t%s\t%s\n",
			line.Dimension, line.Used, line.Unit, limit, line.Enforceable, line.Coverage.Status, line.Coverage.Source)
	}
	_ = writer.Flush()
	for _, disclosure := range summary.Disclosures {
		fmt.Fprintf(output, "\nNote: %s\n", disclosure)
	}
}
