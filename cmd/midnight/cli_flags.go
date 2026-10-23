// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"flag"
	"strconv"
	"strings"
	"time"
)

// The CLI names one concept one way. Historic spellings keep working as
// hidden aliases so existing scripts and muscle memory do not break, but
// only the canonical spelling appears in --help and in docs/cli/flags.md.
//
//	--project-id      (alias: --project)
//	--environment-id  (aliases: --environment, --env)
//	--service-id      (alias: --service)
//	--api-base        (alias: --endpoint)
//	--limit           (aliases: --n, --tail)
//	--follow          (alias: --f)
//	--search          (alias: --q)
//	--output json     (alias: --json)
const (
	flagProjectID     = "project-id"
	flagEnvironmentID = "environment-id"
	flagServiceID     = "service-id"
	flagAPIBase       = "api-base"
	flagLimit         = "limit"
	flagFollow        = "follow"
	flagSearch        = "search"
)

// cliAliasFlag marks a flag definition that exists only for compatibility.
// Alias flags parse exactly like their canonical spelling but are hidden
// from --help so one concept reads one way.
type cliAliasFlag interface {
	canonicalFlagName() string
}

type stringAliasValue struct {
	target    *string
	canonical string
}

func (v *stringAliasValue) String() string {
	if v == nil || v.target == nil {
		return ""
	}
	return *v.target
}

func (v *stringAliasValue) Set(value string) error {
	*v.target = value
	return nil
}

func (v *stringAliasValue) canonicalFlagName() string { return v.canonical }

type boolAliasValue struct {
	target    *bool
	canonical string
}

func (v *boolAliasValue) String() string {
	if v == nil || v.target == nil {
		return "false"
	}
	return strconv.FormatBool(*v.target)
}

func (v *boolAliasValue) Set(value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	*v.target = parsed
	return nil
}

func (v *boolAliasValue) IsBoolFlag() bool { return true }

func (v *boolAliasValue) canonicalFlagName() string { return v.canonical }

type intAliasValue struct {
	target    *int
	canonical string
}

func (v *intAliasValue) String() string {
	if v == nil || v.target == nil {
		return "0"
	}
	return strconv.Itoa(*v.target)
}

func (v *intAliasValue) Set(value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return err
	}
	*v.target = parsed
	return nil
}

func (v *intAliasValue) canonicalFlagName() string { return v.canonical }

type int64AliasValue struct {
	target    *int64
	canonical string
}

func (v *int64AliasValue) String() string {
	if v == nil || v.target == nil {
		return "0"
	}
	return strconv.FormatInt(*v.target, 10)
}

func (v *int64AliasValue) Set(value string) error {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return err
	}
	*v.target = parsed
	return nil
}

func (v *int64AliasValue) canonicalFlagName() string { return v.canonical }

type durationAliasValue struct {
	target    *time.Duration
	canonical string
}

func (v *durationAliasValue) String() string {
	if v == nil || v.target == nil {
		return "0s"
	}
	return v.target.String()
}

func (v *durationAliasValue) Set(value string) error {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*v.target = parsed
	return nil
}

func (v *durationAliasValue) canonicalFlagName() string { return v.canonical }

// cliString defines a canonical string flag plus any hidden aliases that
// write through to the same value.
func cliString(fs *flag.FlagSet, name, value, usage string, aliases ...string) *string {
	target := fs.String(name, value, usage)
	for _, alias := range aliases {
		fs.Var(&stringAliasValue{target: target, canonical: name}, alias, "alias for --"+name)
	}
	return target
}

// cliStringVar is cliString for handlers that already own the destination.
func cliStringVar(fs *flag.FlagSet, target *string, name, value, usage string, aliases ...string) {
	fs.StringVar(target, name, value, usage)
	for _, alias := range aliases {
		fs.Var(&stringAliasValue{target: target, canonical: name}, alias, "alias for --"+name)
	}
}

func cliBool(fs *flag.FlagSet, name string, value bool, usage string, aliases ...string) *bool {
	target := fs.Bool(name, value, usage)
	for _, alias := range aliases {
		fs.Var(&boolAliasValue{target: target, canonical: name}, alias, "alias for --"+name)
	}
	return target
}

func cliInt(fs *flag.FlagSet, name string, value int, usage string, aliases ...string) *int {
	target := fs.Int(name, value, usage)
	for _, alias := range aliases {
		fs.Var(&intAliasValue{target: target, canonical: name}, alias, "alias for --"+name)
	}
	return target
}

func cliInt64(fs *flag.FlagSet, name string, value int64, usage string, aliases ...string) *int64 {
	target := fs.Int64(name, value, usage)
	for _, alias := range aliases {
		fs.Var(&int64AliasValue{target: target, canonical: name}, alias, "alias for --"+name)
	}
	return target
}

func cliDuration(fs *flag.FlagSet, name string, value time.Duration, usage string, aliases ...string) *time.Duration {
	target := fs.Duration(name, value, usage)
	for _, alias := range aliases {
		fs.Var(&durationAliasValue{target: target, canonical: name}, alias, "alias for --"+name)
	}
	return target
}

// Shared scope flags. Every command that names a project, environment, or
// service uses these so the spelling never drifts again.

func cliProjectIDFlag(fs *flag.FlagSet, usage string) *string {
	return cliString(fs, flagProjectID, "", usage, "project")
}

func cliEnvironmentIDFlag(fs *flag.FlagSet, usage string) *string {
	return cliString(fs, flagEnvironmentID, "", usage, "environment", "env")
}

func cliServiceIDFlag(fs *flag.FlagSet, usage string) *string {
	return cliString(fs, flagServiceID, "", usage, "service")
}

func cliAPIBaseFlag(fs *flag.FlagSet, usage string) *string {
	return cliString(fs, flagAPIBase, "", usage, "endpoint")
}

func cliLimitFlag(fs *flag.FlagSet, value int, usage string) *int {
	return cliInt(fs, flagLimit, value, usage, "n", "tail")
}

func cliFollowFlag(fs *flag.FlagSet, usage string) *bool {
	return cliBool(fs, flagFollow, false, usage, "f")
}

func cliSearchFlag(fs *flag.FlagSet, usage string) *string {
	return cliString(fs, flagSearch, "", usage, "q")
}

// cliIdempotencyKeyFlag is the one spelling for a caller-supplied replay key.
func cliIdempotencyKeyFlag(fs *flag.FlagSet, usage string) *string {
	return fs.String("idempotency-key", "", usage)
}

// flagSupplied reports whether the user passed the canonical flag or any of
// its aliases. Handlers that distinguish "set to empty" from "not set" must
// use this instead of fs.Visit on the canonical name alone.
func flagSupplied(fs *flag.FlagSet, names ...string) bool {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	supplied := false
	fs.Visit(func(defined *flag.Flag) {
		if wanted[defined.Name] {
			supplied = true
			return
		}
		if alias, ok := defined.Value.(cliAliasFlag); ok && wanted[alias.canonicalFlagName()] {
			supplied = true
		}
	})
	return supplied
}

// canonicalFlagUsage renders a flag list for a usage line, canonical names
// only, so error text and --help agree.
func canonicalFlagUsage(fs *flag.FlagSet) string {
	var names []string
	fs.VisitAll(func(defined *flag.Flag) {
		if _, alias := defined.Value.(cliAliasFlag); alias {
			return
		}
		names = append(names, "--"+defined.Name)
	})
	return strings.Join(names, " ")
}
