// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

type cliVolume struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ServiceID     string `json:"service_id"`
	Name          string `json:"name"`
	SizeBytes     int64  `json:"size_bytes"`
	Status        string `json:"status"`
	MountPath     string `json:"mount_path"`
}

type cliVolumeBackup struct {
	ID          string `json:"id"`
	VolumeID    string `json:"volume_id"`
	State       string `json:"state"`
	TriggerKind string `json:"trigger_kind"`
	ObjectBytes int64  `json:"object_bytes"`
	CreatedAt   string `json:"created_at"`
}

type cliVolumeRestore struct {
	ID                  string `json:"id"`
	BackupID            string `json:"backup_id"`
	SourceVolumeID      string `json:"source_volume_id"`
	State               string `json:"state"`
	ReplacementVolumeID string `json:"replacement_volume_id"`
	SourceRetained      bool   `json:"source_retained"`
}

type cliVolumePolicy struct {
	ID               string `json:"id"`
	VolumeID         string `json:"volume_id"`
	ScheduleKind     string `json:"schedule_kind"`
	RetentionSeconds int64  `json:"retention_seconds"`
	NextRunAt        string `json:"next_run_at"`
}

type cliVolumeBackupList struct {
	Data []cliVolumeBackup `json:"data"`
}

type cliVolumeRestoreList struct {
	Data []cliVolumeRestore `json:"data"`
}

func runVolume(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printVolumeUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runVolumeList(args[1:], stdout, stderr)
	case "create":
		return runVolumeCreate(args[1:], stdout, stderr)
	case "backup":
		return runVolumeBackup(args[1:], stdout, stderr)
	case "restore":
		return runVolumeRestore(args[1:], stdout, stderr)
	case "policy":
		return runVolumePolicy(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight volume: unknown subcommand %q\n", args[0])
		printVolumeUsage(stderr)
		return 64
	}
}

func runVolumeList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight volume list", stderr)
	f := defineVolumeListFlags(fs)
	projectID, environmentID, serviceID := f.projectID, f.environmentID, f.serviceID
	if err := fs.Parse(args); err != nil {
		return 64
	}
	query := url.Values{}
	if *projectID != "" {
		query.Set("project_id", *projectID)
	}
	if *environmentID != "" {
		query.Set("environment_id", *environmentID)
	}
	if *serviceID != "" {
		query.Set("service_id", *serviceID)
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume list: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/volumes"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var values []cliVolume
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &values)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, values,
		func(value cliVolume) string { return value.ID },
		func(output io.Writer, rows []cliVolume) {
			for _, value := range rows {
				fmt.Fprintf(output, "%s\t%s\t%s\t%s\t%s\n", value.ID, value.Name, value.MountPath, byteLabel(value.SizeBytes), value.Status)
			}
		},
	)
}

func runVolumeCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight volume create", stderr)
	f := defineVolumeCreateFlags(fs)
	projectID, environmentID, serviceID, name := f.projectID, f.environmentID, f.serviceID, f.name
	mountPath, sizeBytes, key := f.mountPath, f.sizeBytes, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *projectID == "" || *environmentID == "" || *serviceID == "" || *name == "" || *mountPath == "" {
		fmt.Fprintln(stderr, "midnight volume create: --project-id, --environment-id, --service-id, --name, and --mount-path are required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume create: %s\n", err)
		return 64
	}
	body := map[string]any{
		"project_id": *projectID, "environment_id": *environmentID,
		"service_id": *serviceID, "name": *name, "mount_path": *mountPath,
		"size_bytes": *sizeBytes,
	}
	path := "/api/v1alpha/volumes"
	resolvedKey, err := resolveMutationIdempotencyKey(*key, client.token, "volume-create", http.MethodPost, path, body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume create: resolve idempotency key: %s\n", err)
		return 64
	}
	var created cliVolume
	raw, err := client.doJSON(http.MethodPost, path, body, resolvedKey, &created)
	if err != nil {
		writeMutationFailure(stderr, "volume create", err, resolvedKey)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "volume created: %s (%s at %s)\n", created.ID, created.Name, created.MountPath)
	return 0
}

func runVolumeBackup(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: midnight volume backup list|create|restore")
		return 64
	}
	switch args[0] {
	case "list":
		return runVolumeBackupList(args[1:], stdout, stderr)
	case "create":
		return runVolumeBackupCreate(args[1:], stdout, stderr)
	case "restore":
		return runVolumeBackupRestore(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight volume backup: unknown subcommand %q\n", args[0])
		return 64
	}
}

func runVolumeBackupList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight volume backup list", stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight volume backup list <volume-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume backup list: %s\n", err)
		return 64
	}
	var list cliVolumeBackupList
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/volumes/"+url.PathEscape(fs.Arg(0))+"/backups", nil, "", &list)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume backup list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, list.Data,
		func(value cliVolumeBackup) string { return value.ID },
		func(output io.Writer, rows []cliVolumeBackup) {
			for _, run := range rows {
				fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", run.ID, run.State, run.TriggerKind, byteLabel(run.ObjectBytes))
			}
		},
	)
}

func runVolumeBackupCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight volume backup create", stderr)
	f := defineVolumeBackupCreateFlags(fs)
	retention, key := f.retention, f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight volume backup create [--retention-days N] <volume-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume backup create: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/volumes/" + url.PathEscape(fs.Arg(0)) + "/backups"
	body := map[string]int{"retention_days": *retention}
	attempt, err := beginPendingMutationAttempt(*key, client.token, "volume-backup-create", http.MethodPost, path, body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume backup create: prepare retry-safe attempt: %s\n", err)
		return 1
	}
	var accepted struct {
		Run   cliVolumeBackup `json:"run"`
		JobID string          `json:"job_id"`
	}
	raw, err := client.doJSON(http.MethodPost, path, body, attempt.Key, &accepted)
	if finishErr := attempt.Finish(err); finishErr != nil {
		fmt.Fprintf(stderr, "midnight volume backup create: %s\n", finishErr)
		return 1
	}
	if err != nil {
		writeMutationFailure(stderr, "volume backup create", err, attempt.Key)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "backup queued: %s (job %s)\n", accepted.Run.ID, accepted.JobID)
	return 0
}

func runVolumeBackupRestore(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight volume backup restore", stderr)
	f := defineVolumeBackupRestoreFlags(fs)
	key := f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight volume backup restore <backup-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume backup restore: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/volume-backups/" + url.PathEscape(fs.Arg(0)) + "/restores"
	body := map[string]any{}
	attempt, err := beginPendingMutationAttempt(*key, client.token, "volume-backup-restore", http.MethodPost, path, body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight volume backup restore: prepare retry-safe attempt: %s\n", err)
		return 1
	}
	var accepted struct {
		Run   cliVolumeRestore `json:"run"`
		JobID string           `json:"job_id"`
	}
	raw, err := client.doJSON(http.MethodPost, path, body, attempt.Key, &accepted)
	if finishErr := attempt.Finish(err); finishErr != nil {
		fmt.Fprintf(stderr, "midnight volume backup restore: %s\n", finishErr)
		return 1
	}
	if err != nil {
		writeMutationFailure(stderr, "volume backup restore", err, attempt.Key)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "restore queued: %s (job %s); apply only after state=staged\n", accepted.Run.ID, accepted.JobID)
	return 0
}

func runVolumeRestore(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: midnight volume restore list|apply")
		return 64
	}
	if args[0] == "list" {
		fs, cfg := newAPIFlagSet("midnight volume restore list", stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return 64
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: midnight volume restore list <volume-id>")
			return 64
		}
		client, err := newCLIAPIClient(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume restore list: %s\n", err)
			return 64
		}
		var list cliVolumeRestoreList
		raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/volumes/"+url.PathEscape(fs.Arg(0))+"/restores", nil, "", &list)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume restore list: %s\n", err)
			return 1
		}
		return renderCLIList(stdout, stderr, cfg.JSON, raw, list.Data,
			func(value cliVolumeRestore) string { return value.ID },
			func(output io.Writer, rows []cliVolumeRestore) {
				for _, run := range rows {
					fmt.Fprintf(output, "%s\t%s\tbackup=%s\treplacement=%s\n", run.ID, run.State, run.BackupID, run.ReplacementVolumeID)
				}
			},
		)
	}
	if args[0] == "apply" {
		fs, cfg := newAPIFlagSet("midnight volume restore apply", stderr)
		key := defineVolumeRestoreApplyFlags(fs).key
		if err := fs.Parse(args[1:]); err != nil {
			return 64
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: midnight volume restore apply <restore-id>")
			return 64
		}
		client, err := newCLIAPIClient(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume restore apply: %s\n", err)
			return 64
		}
		path := "/api/v1alpha/volume-restores/" + url.PathEscape(fs.Arg(0)) + "/apply"
		body := map[string]any{}
		resolvedKey, err := resolveMutationIdempotencyKey(*key, client.token, "volume-restore-apply", http.MethodPost, path, body)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume restore apply: resolve idempotency key: %s\n", err)
			return 64
		}
		var result struct {
			Restore cliVolumeRestore `json:"restore"`
			Created bool             `json:"created"`
		}
		raw, err := client.doJSON(http.MethodPost, path, body, resolvedKey, &result)
		if err != nil {
			writeMutationFailure(stderr, "volume restore apply", err, resolvedKey)
			return 1
		}
		if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
			return code
		}
		fmt.Fprintf(stdout, "restore applied: %s; immutable redeploy queued\n", result.Restore.ID)
		return 0
	}
	fmt.Fprintf(stderr, "midnight volume restore: unknown subcommand %q\n", args[0])
	return 64
}

func runVolumePolicy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: midnight volume policy list|create|delete")
		return 64
	}
	switch args[0] {
	case "list":
		fs, cfg := newAPIFlagSet("midnight volume policy list", stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return 64
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: midnight volume policy list <volume-id>")
			return 64
		}
		client, err := newCLIAPIClient(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume policy list: %s\n", err)
			return 64
		}
		var policies []cliVolumePolicy
		raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/volumes/"+url.PathEscape(fs.Arg(0))+"/backup-policies", nil, "", &policies)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume policy list: %s\n", err)
			return 1
		}
		return renderCLIList(stdout, stderr, cfg.JSON, raw, policies,
			func(value cliVolumePolicy) string { return value.ID },
			func(output io.Writer, rows []cliVolumePolicy) {
				for _, policy := range rows {
					fmt.Fprintf(output, "%s\t%s\tretention=%dd\tnext=%s\n",
						policy.ID, policy.ScheduleKind, policy.RetentionSeconds/86400, policy.NextRunAt)
				}
			},
		)
	case "create":
		fs, cfg := newAPIFlagSet("midnight volume policy create", stderr)
		policyFlags := defineVolumePolicyCreateFlags(fs)
		schedule, retention, key := policyFlags.schedule, policyFlags.retention, policyFlags.key
		if err := fs.Parse(args[1:]); err != nil {
			return 64
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: midnight volume policy create [--schedule daily|weekly|monthly] [--retention-days N] <volume-id>")
			return 64
		}
		client, err := newCLIAPIClient(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume policy create: %s\n", err)
			return 64
		}
		path := "/api/v1alpha/volumes/" + url.PathEscape(fs.Arg(0)) + "/backup-policies"
		body := map[string]any{"schedule_kind": *schedule, "retention_days": *retention}
		resolvedKey, err := resolveMutationIdempotencyKey(*key, client.token, "volume-policy-create", http.MethodPost, path, body)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume policy create: resolve idempotency key: %s\n", err)
			return 64
		}
		var created cliVolumePolicy
		raw, err := client.doJSON(http.MethodPost, path, body, resolvedKey, &created)
		if err != nil {
			writeMutationFailure(stderr, "volume policy create", err, resolvedKey)
			return 1
		}
		if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
			return code
		}
		fmt.Fprintf(stdout, "backup policy created: %s (%s)\n", created.ID, created.ScheduleKind)
		return 0
	case "delete":
		fs, cfg := newAPIFlagSet("midnight volume policy delete", stderr)
		key := defineVolumePolicyDeleteFlags(fs).key
		if err := fs.Parse(args[1:]); err != nil {
			return 64
		}
		if fs.NArg() != 2 {
			fmt.Fprintln(stderr, "usage: midnight volume policy delete <volume-id> <policy-id>")
			return 64
		}
		client, err := newCLIAPIClient(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume policy delete: %s\n", err)
			return 64
		}
		path := "/api/v1alpha/volumes/" + url.PathEscape(fs.Arg(0)) + "/backup-policies/" + url.PathEscape(fs.Arg(1))
		resolvedKey, err := resolveMutationIdempotencyKey(*key, client.token, "volume-policy-delete", http.MethodDelete, path, nil)
		if err != nil {
			fmt.Fprintf(stderr, "midnight volume policy delete: resolve idempotency key: %s\n", err)
			return 64
		}
		if _, err := client.doJSON(http.MethodDelete, path, nil, resolvedKey, nil); err != nil {
			writeMutationFailure(stderr, "volume policy delete", err, resolvedKey)
			return 1
		}
		fmt.Fprintf(stdout, "backup policy deleted: %s\n", fs.Arg(1))
		return 0
	default:
		fmt.Fprintf(stderr, "midnight volume policy: unknown subcommand %q\n", args[0])
		return 64
	}
}

func byteLabel(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	const unit = int64(1024)
	if value < unit {
		return strconv.FormatInt(value, 10) + " B"
	}
	div, exp := unit, 0
	for n := value / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

func printVolumeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midnight volume list|create|backup|restore|policy")
	fmt.Fprintln(w, "  backup list|create|restore; restore list|apply; policy list|create|delete")
}

type volumeListFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
}

func defineVolumeListFlags(fs *flag.FlagSet) volumeListFlags {
	return volumeListFlags{
		projectID:     cliProjectIDFlag(fs, "project id"),
		environmentID: cliEnvironmentIDFlag(fs, "environment id"),
		serviceID:     cliServiceIDFlag(fs, "service id"),
	}
}

type volumeCreateFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
	name          *string
	mountPath     *string
	sizeBytes     *int64
	key           *string
}

func defineVolumeCreateFlags(fs *flag.FlagSet) volumeCreateFlags {
	return volumeCreateFlags{
		projectID:     cliProjectIDFlag(fs, "project id"),
		environmentID: cliEnvironmentIDFlag(fs, "environment id"),
		serviceID:     cliServiceIDFlag(fs, "service id"),
		name:          fs.String("name", "", "volume name"),
		mountPath:     fs.String("mount-path", "", "absolute container mount path"),
		sizeBytes:     fs.Int64("size-bytes", 0, "requested capacity in bytes"),
		key:           cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact create intent when omitted)"),
	}
}

type volumeBackupCreateFlags struct {
	retention *int
	key       *string
}

func defineVolumeBackupCreateFlags(fs *flag.FlagSet) volumeBackupCreateFlags {
	return volumeBackupCreateFlags{
		retention: fs.Int("retention-days", 30, "retention in days (0 keeps operator-managed)"),
		key:       cliIdempotencyKeyFlag(fs, "stable retry key (a pending attempt is reused when omitted)"),
	}
}

type volumeBackupRestoreFlags struct {
	key *string
}

func defineVolumeBackupRestoreFlags(fs *flag.FlagSet) volumeBackupRestoreFlags {
	return volumeBackupRestoreFlags{
		key: cliIdempotencyKeyFlag(fs, "stable retry key (a pending attempt is reused when omitted)"),
	}
}

type volumeRestoreApplyFlags struct {
	key *string
}

func defineVolumeRestoreApplyFlags(fs *flag.FlagSet) volumeRestoreApplyFlags {
	return volumeRestoreApplyFlags{
		key: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact apply intent when omitted)"),
	}
}

type volumePolicyCreateFlags struct {
	schedule  *string
	retention *int
	key       *string
}

func defineVolumePolicyCreateFlags(fs *flag.FlagSet) volumePolicyCreateFlags {
	return volumePolicyCreateFlags{
		schedule:  fs.String("schedule", "daily", "daily|weekly|monthly"),
		retention: fs.Int("retention-days", 30, "retention in days"),
		key:       cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact create intent when omitted)"),
	}
}

type volumePolicyDeleteFlags struct {
	key *string
}

func defineVolumePolicyDeleteFlags(fs *flag.FlagSet) volumePolicyDeleteFlags {
	return volumePolicyDeleteFlags{
		key: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact delete intent when omitted)"),
	}
}
