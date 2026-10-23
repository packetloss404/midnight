package main

import (
	"fmt"
	"io"
)

// runAudit dispatches `midnight audit <subcommand>` invocations.
// Returns the exit code main.go should pass to os.Exit.
func runAudit(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "midnight audit: subcommand required")
		fmt.Fprintln(stderr, "  midnight audit verify [--from-jsonl PATH] [--public-key FILE] [--json]")
		return auditVerifyExitUsage
	}
	switch args[0] {
	case "verify":
		return runAuditVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight audit: unknown subcommand %q\n", args[0])
		return auditVerifyExitUsage
	}
}
