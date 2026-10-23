// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// Template is the one-click service-bundle manifest. A template names a set
// of services (image + env + optional volume + healthcheck) that, when
// materialized, create real `service` rows, variables, and volume
// attachments wired together.
//
// Embedded PostgreSQL, Redis, MySQL, and MongoDB manifests are the official
// single-node database set. Community manifests reuse this resource shape but
// pass the stricter provenance validator in community.go and are never treated
// as official merely because their payload resembles a built-in template.
type Template struct {
	Slug    string `json:"slug" yaml:"slug"`
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
	Summary string `json:"summary" yaml:"summary"`
	// Tags is a small set of marketplace-style descriptors used by the
	// REST list endpoint. Optional.
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	// ConsumerDocs is a short string the operator doc + connection-info
	// surfaces show ("Reference DATABASE_URL from a sibling service with
	// ${{Postgres.DATABASE_URL}}"). Helps document the reference-variable
	// integration without coupling the materializer to that track.
	ConsumerDocs string            `json:"consumer_docs,omitempty" yaml:"consumer_docs,omitempty"`
	Services     []TemplateService `json:"services" yaml:"services"`
}

// TemplateService describes one Docker-image-backed service the template
// will create. Slug is the *suggested* slug; the materializer disambiguates
// when a collision occurs.
type TemplateService struct {
	Slug        string                 `json:"slug" yaml:"slug"`
	Image       string                 `json:"image" yaml:"image"`
	Args        []string               `json:"args,omitempty" yaml:"args,omitempty"`
	Env         map[string]TemplateEnv `json:"env,omitempty" yaml:"env,omitempty"`
	Volume      *TemplateVolume        `json:"volume,omitempty" yaml:"volume,omitempty"`
	Healthcheck *TemplateHealthcheck   `json:"healthcheck,omitempty" yaml:"healthcheck,omitempty"`
	Ports       []TemplatePort         `json:"ports,omitempty" yaml:"ports,omitempty"`
}

// TemplateVolume is the volume-worker integration handoff. The
// materializer records the declared attachment via a volume API call
// once the worker integration lands; for now we capture intent so the
// returned CreatedResource set includes the volume row identifier.
type TemplateVolume struct {
	Name      string `json:"name" yaml:"name"`
	MountPath string `json:"mount_path" yaml:"mount_path"`
	SizeBytes int64  `json:"size_bytes" yaml:"size_bytes"`
}

// TemplateHealthcheck is a runtime healthcheck declaration. The
// materializer copies this through to the service spec; the runtime
// adapter consumes it later.
type TemplateHealthcheck struct {
	// Cmd is the shell-style command the runtime executes. Variables
	// in the form $VAR are expanded by the container shell at runtime.
	Cmd string `json:"cmd" yaml:"cmd"`
}

// TemplatePort lists a runtime port the template requires. Not
// auto-exposed externally; the private-networking track handles
// DNS-only access via {slug}.midnight.internal.
type TemplatePort struct {
	Port     int    `json:"port" yaml:"port"`
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
}

// EnvKind discriminates the TemplateEnv sum type. The wire shape is
// `{kind: "literal"|"generated"|"computed_url", ...}`.
type EnvKind string

const (
	// EnvKindLiteral copies the provided value into the variable
	// vault verbatim. Used for non-secret config like PGUSER.
	EnvKindLiteral EnvKind = "literal"
	// EnvKindGenerated produces a fresh random value at materialize
	// time. Two GeneratedKinds are supported: password and uuid.
	EnvKindGenerated EnvKind = "generated"
	// EnvKindComputedURL renders a Go-template-style string against
	// the other variables in the same service, evaluated after all
	// Literals and Generateds have been resolved.
	EnvKindComputedURL EnvKind = "computed_url"
)

// GeneratedKind names the generator used for EnvKindGenerated values.
type GeneratedKind string

const (
	// GeneratedKindPassword produces a random alphanumeric password.
	// Default length is 32 bytes; the materializer treats Length=0
	// as 32.
	GeneratedKindPassword GeneratedKind = "password"
	// GeneratedKindUUID produces a v4-shaped UUID.
	GeneratedKindUUID GeneratedKind = "uuid"
)

// TemplateEnv is the sum type for env values declared on a template
// service.
//
// Wire shape:
//
//	{"kind":"literal","value":"postgres"}
//	{"kind":"generated","generated_kind":"password","length":32}
//	{"kind":"computed_url","template":"postgres://{{.PGUSER}}:{{.PGPASSWORD}}@{{.PGHOST}}:{{.PGPORT}}/{{.PGDATABASE}}"}
//
// The Go shape uses pointer fields so JSON consumers can omit the
// non-applicable branches.
type TemplateEnv struct {
	Kind          EnvKind       `json:"kind" yaml:"kind"`
	Value         string        `json:"value,omitempty" yaml:"value,omitempty"`
	GeneratedKind GeneratedKind `json:"generated_kind,omitempty" yaml:"generated_kind,omitempty"`
	Length        int           `json:"length,omitempty" yaml:"length,omitempty"`
	Template      string        `json:"template,omitempty" yaml:"template,omitempty"`
	// Secret marks the variable as a sealed secret in the vault
	// (e.g. POSTGRES_PASSWORD). Computed URLs that embed a secret
	// inherit Secret=true.
	Secret bool `json:"secret,omitempty" yaml:"secret,omitempty"`
}

// CreatedResource is one row produced by Materialize: a service, a
// variable, or a volume row. The materializer returns the ordered set
// so callers can audit and surface progress.
type CreatedResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ErrTemplateNotFound is returned by Lookup when the slug is unknown.
var ErrTemplateNotFound = errors.New("template not found")

// ErrTemplateInvalid is returned when a manifest fails the on-load
// validator (an unknown env kind, an unresolvable {{.Ref}} in a
// computed URL, etc).
var ErrTemplateInvalid = errors.New("template invalid")

// Builtins returns the embedded one-click templates in slug order.
func Builtins() []Template {
	out := []Template{Postgres16(), Redis7(), MySQL8(), MongoDB7()}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// Lookup finds a built-in template by slug. The match is case-insensitive
// and tolerates a leading/trailing whitespace.
func Lookup(slug string) (Template, error) {
	want := strings.ToLower(strings.TrimSpace(slug))
	for _, tmpl := range Builtins() {
		if strings.EqualFold(tmpl.Slug, want) {
			return tmpl, nil
		}
	}
	return Template{}, fmt.Errorf("%w: %s", ErrTemplateNotFound, slug)
}

// ValidateTemplate runs the minimal "parses and references exist" check
// required by the scope guard. It returns the first error encountered.
func ValidateTemplate(t Template) error {
	if strings.TrimSpace(t.Slug) == "" {
		return fmt.Errorf("%w: slug is required", ErrTemplateInvalid)
	}
	if strings.TrimSpace(t.Version) == "" {
		return fmt.Errorf("%w: version is required (template %s)", ErrTemplateInvalid, t.Slug)
	}
	if len(t.Services) == 0 {
		return fmt.Errorf("%w: template %s declares no services", ErrTemplateInvalid, t.Slug)
	}
	for _, svc := range t.Services {
		if strings.TrimSpace(svc.Slug) == "" {
			return fmt.Errorf("%w: template %s has a service without a slug", ErrTemplateInvalid, t.Slug)
		}
		if strings.TrimSpace(svc.Image) == "" {
			return fmt.Errorf("%w: template %s service %s has no image", ErrTemplateInvalid, t.Slug, svc.Slug)
		}
		if err := validateServiceEnv(svc); err != nil {
			return err
		}
	}
	return nil
}

func validateServiceEnv(svc TemplateService) error {
	// Pre-collect the names of literal/generated env vars so we can
	// confirm every computed-URL template references only known names.
	known := make(map[string]struct{}, len(svc.Env)+1)
	for name, env := range svc.Env {
		switch env.Kind {
		case EnvKindLiteral, EnvKindGenerated:
			known[name] = struct{}{}
		case EnvKindComputedURL:
			// Resolved in a second pass.
		default:
			return fmt.Errorf("%w: env %q has unknown kind %q", ErrTemplateInvalid, name, env.Kind)
		}
		if env.Kind == EnvKindGenerated {
			switch env.GeneratedKind {
			case GeneratedKindPassword, GeneratedKindUUID:
			default:
				return fmt.Errorf("%w: env %q generated kind %q is unsupported", ErrTemplateInvalid, name, env.GeneratedKind)
			}
		}
	}
	// MIDNIGHT_PRIVATE_DOMAIN is supplied by the private-networking
	// track at materialize time; computed URLs may reference it.
	known["MIDNIGHT_PRIVATE_DOMAIN"] = struct{}{}
	for name, env := range svc.Env {
		if env.Kind != EnvKindComputedURL {
			continue
		}
		tmpl, err := template.New(name).Option("missingkey=error").Parse(env.Template)
		if err != nil {
			return fmt.Errorf("%w: env %q template parse: %v", ErrTemplateInvalid, name, err)
		}
		// Render against a probe map containing only the declared
		// names; if the template references an undeclared name the
		// rendering errors out.
		probe := make(map[string]string, len(known))
		for n := range known {
			probe[n] = "x"
		}
		var sink strings.Builder
		if err := tmpl.Execute(&sink, probe); err != nil {
			return fmt.Errorf("%w: env %q template references undeclared variable: %v", ErrTemplateInvalid, name, err)
		}
	}
	return nil
}

// passwordAlphabet keeps the random password in the
// alphanumeric subset so the value is safe to embed inside a
// percent-encoded URL without further escaping.
const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// GeneratePassword returns a random alphanumeric password of the
// requested length. Length defaults to 32 when <= 0. It is exported
// so the materializer + tests share a single implementation.
func GeneratePassword(length int) (string, error) {
	if length <= 0 {
		length = 32
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i, b := range buf {
		out[i] = passwordAlphabet[int(b)%len(passwordAlphabet)]
	}
	return string(out), nil
}

// GenerateUUID produces an RFC 4122 v4 UUID string.
func GenerateUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0F) | 0x40 // version 4
	b[8] = (b[8] & 0x3F) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Postgres16 returns the one-click Postgres 16 template.
//
// The materialize-time output is:
//   - service slug `postgres` running the official digest-pinned Postgres image
//   - 1 GiB volume mounted at /var/lib/postgresql/data
//   - sealed POSTGRES_PASSWORD (32-char alphanumeric)
//   - literal PGUSER=postgres, PGDATABASE=midnight, PGPORT=5432
//   - PGHOST=${MIDNIGHT_PRIVATE_DOMAIN} resolved at materialize time
//     to `<service-slug>.midnight.internal` (private-networking track)
//   - DATABASE_URL assembled from the values above
//
// A consumer service references this from a sibling by using the exact
// service slug returned in created_services[].slug, for example
// `${{orders-database-abcd1234.DATABASE_URL}}`.
func Postgres16() Template {
	return Template{
		Slug:    "postgres",
		Name:    "PostgreSQL 16",
		Version: "16.14",
		Summary: "PostgreSQL 16 (alpine) with a 1 GiB attached volume, a generated password, and a fully assembled DATABASE_URL.",
		Tags:    []string{"database", "postgres", "official"},
		ConsumerDocs: "Reference DATABASE_URL from a sibling service by using the exact slug " +
			"returned in created_services[].slug, for example ${{orders-database-abcd1234.DATABASE_URL}}.",
		Services: []TemplateService{{
			Slug:  "postgres",
			Image: "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20",
			Env: map[string]TemplateEnv{
				"POSTGRES_PASSWORD": {
					Kind:          EnvKindGenerated,
					GeneratedKind: GeneratedKindPassword,
					Length:        32,
					Secret:        true,
				},
				"POSTGRES_USER": {Kind: EnvKindLiteral, Value: "postgres"},
				"POSTGRES_DB":   {Kind: EnvKindLiteral, Value: "midnight"},
				"PGUSER":        {Kind: EnvKindLiteral, Value: "postgres"},
				"PGDATABASE":    {Kind: EnvKindLiteral, Value: "midnight"},
				"PGPORT":        {Kind: EnvKindLiteral, Value: "5432"},
				"PGHOST":        {Kind: EnvKindComputedURL, Template: "{{.MIDNIGHT_PRIVATE_DOMAIN}}"},
				"PGPASSWORD":    {Kind: EnvKindComputedURL, Template: "{{.POSTGRES_PASSWORD}}", Secret: true},
				"DATABASE_URL": {
					Kind:     EnvKindComputedURL,
					Template: "postgres://{{.PGUSER}}:{{.POSTGRES_PASSWORD}}@{{.MIDNIGHT_PRIVATE_DOMAIN}}:{{.PGPORT}}/{{.PGDATABASE}}",
					Secret:   true,
				},
			},
			Volume: &TemplateVolume{
				Name:      "data",
				MountPath: "/var/lib/postgresql/data",
				SizeBytes: 1 << 30, // 1 GiB
			},
			Healthcheck: &TemplateHealthcheck{Cmd: "pg_isready -U $PGUSER"},
			Ports:       []TemplatePort{{Port: 5432, Protocol: "tcp"}},
		}},
	}
}

// Redis7 returns the one-click Redis 7 template.
//
// The materialize-time output is:
//   - service slug `redis` running the exact-pinned Redis 7.2.5 image with a
//     protected generated config and the official privilege-dropping entrypoint
//   - 256 MiB volume mounted at /data
//   - sealed REDIS_PASSWORD (32-char alphanumeric)
//   - REDIS_URL assembled as redis://:<password>@<private-domain>:6379
func Redis7() Template {
	return Template{
		Slug:    "redis",
		Name:    "Redis 7",
		Version: "7.2.5",
		Summary: "Redis 7 (alpine) with a 256 MiB attached volume, a generated password, and a fully assembled REDIS_URL.",
		Tags:    []string{"cache", "database", "redis", "official"},
		ConsumerDocs: "Reference REDIS_URL from a sibling service by using the exact slug " +
			"returned in created_services[].slug.",
		Services: []TemplateService{{
			Slug:  "redis",
			Image: "redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e",
			Args: []string{
				"/bin/sh", "-lc",
				`case "$REDIS_PASSWORD" in ""|*[!A-Za-z0-9]*) echo "REDIS_PASSWORD must be non-empty alphanumeric" >&2; exit 64;; esac; mkdir -p /run/midnight; chown root:redis /run/midnight; chmod 0710 /run/midnight; rm -f /run/midnight/redis.conf; (umask 027; set -C; printf 'appendonly yes\nrequirepass %s\n' "$REDIS_PASSWORD" > /run/midnight/redis.conf) || exit 1; chown root:redis /run/midnight/redis.conf; chmod 0640 /run/midnight/redis.conf; exec /usr/local/bin/docker-entrypoint.sh redis-server /run/midnight/redis.conf`,
			},
			Env: map[string]TemplateEnv{
				"REDIS_PASSWORD": {
					Kind:          EnvKindGenerated,
					GeneratedKind: GeneratedKindPassword,
					Length:        32,
					Secret:        true,
				},
				"REDIS_PORT": {Kind: EnvKindLiteral, Value: "6379"},
				"REDIS_URL": {
					Kind:     EnvKindComputedURL,
					Template: "redis://:{{.REDIS_PASSWORD}}@{{.MIDNIGHT_PRIVATE_DOMAIN}}:{{.REDIS_PORT}}/0",
					Secret:   true,
				},
			},
			Volume: &TemplateVolume{
				Name:      "data",
				MountPath: "/data",
				SizeBytes: 256 << 20, // 256 MiB
			},
			Healthcheck: &TemplateHealthcheck{Cmd: `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping`},
			Ports:       []TemplatePort{{Port: 6379, Protocol: "tcp"}},
		}},
	}
}

// MySQL8 returns the verified single-node MySQL 8.4 template.
func MySQL8() Template {
	return Template{
		Slug:    "mysql",
		Name:    "MySQL 8.4",
		Version: "8.4.0",
		Summary: "MySQL 8.4 with a 1 GiB attached volume, generated root/application credentials, and MYSQL_URL/DATABASE_URL.",
		Tags:    []string{"database", "mysql", "official"},
		ConsumerDocs: "Reference MYSQL_URL or DATABASE_URL from a sibling service by using the exact slug " +
			"returned in created_services[].slug.",
		Services: []TemplateService{{
			Slug:  "mysql",
			Image: "mysql:8.4.0@sha256:dab7049abafe3a0e12cbe5e49050cf149881c0cd9665c289e5808b9dad39c9e0",
			Env: map[string]TemplateEnv{
				"MYSQL_ROOT_PASSWORD": {
					Kind:          EnvKindGenerated,
					GeneratedKind: GeneratedKindPassword,
					Length:        32,
					Secret:        true,
				},
				"MYSQL_PASSWORD": {
					Kind:          EnvKindGenerated,
					GeneratedKind: GeneratedKindPassword,
					Length:        32,
					Secret:        true,
				},
				"MYSQL_DATABASE": {Kind: EnvKindLiteral, Value: "midnight"},
				"MYSQL_USER":     {Kind: EnvKindLiteral, Value: "midnight"},
				"MYSQL_HOST":     {Kind: EnvKindComputedURL, Template: "{{.MIDNIGHT_PRIVATE_DOMAIN}}"},
				"MYSQL_PORT":     {Kind: EnvKindLiteral, Value: "3306"},
				"MYSQL_URL": {
					Kind:     EnvKindComputedURL,
					Template: "mysql://{{.MYSQL_USER}}:{{.MYSQL_PASSWORD}}@{{.MIDNIGHT_PRIVATE_DOMAIN}}:{{.MYSQL_PORT}}/{{.MYSQL_DATABASE}}",
					Secret:   true,
				},
				"DATABASE_URL": {
					Kind:     EnvKindComputedURL,
					Template: "mysql://{{.MYSQL_USER}}:{{.MYSQL_PASSWORD}}@{{.MIDNIGHT_PRIVATE_DOMAIN}}:{{.MYSQL_PORT}}/{{.MYSQL_DATABASE}}",
					Secret:   true,
				},
			},
			Volume: &TemplateVolume{Name: "data", MountPath: "/var/lib/mysql", SizeBytes: 1 << 30},
			Healthcheck: &TemplateHealthcheck{
				Cmd: `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqladmin ping -h 127.0.0.1 -u root`,
			},
			Ports: []TemplatePort{{Port: 3306, Protocol: "tcp"}},
		}},
	}
}

// MongoDB7 returns the verified single-node MongoDB 7 template. Replica sets
// and sharded clusters remain explicitly outside this template's capability.
func MongoDB7() Template {
	return Template{
		Slug:    "mongodb",
		Name:    "MongoDB 7",
		Version: "7.0.9",
		Summary: "MongoDB 7 single-node with a 1 GiB attached volume, generated credentials, and MONGO_URL/MONGODB_URL.",
		Tags:    []string{"database", "mongodb", "official", "single-node"},
		ConsumerDocs: "Reference MONGO_URL or MONGODB_URL from a sibling service by using the exact slug " +
			"returned in created_services[].slug. Replica-set and sharded modes are not claimed.",
		Services: []TemplateService{{
			Slug:  "mongodb",
			Image: "mongo:7.0.9@sha256:97aac78a80553735b3d9b9b7212803468781b4859645f892a3d04e6b621a7b77",
			Env: map[string]TemplateEnv{
				"MONGO_INITDB_ROOT_USERNAME": {Kind: EnvKindLiteral, Value: "midnight"},
				"MONGO_INITDB_ROOT_PASSWORD": {
					Kind:          EnvKindGenerated,
					GeneratedKind: GeneratedKindPassword,
					Length:        32,
					Secret:        true,
				},
				"MONGO_INITDB_DATABASE": {Kind: EnvKindLiteral, Value: "midnight"},
				"MONGO_HOST":            {Kind: EnvKindComputedURL, Template: "{{.MIDNIGHT_PRIVATE_DOMAIN}}"},
				"MONGO_PORT":            {Kind: EnvKindLiteral, Value: "27017"},
				"MONGO_URL": {
					Kind:     EnvKindComputedURL,
					Template: "mongodb://{{.MONGO_INITDB_ROOT_USERNAME}}:{{.MONGO_INITDB_ROOT_PASSWORD}}@{{.MIDNIGHT_PRIVATE_DOMAIN}}:{{.MONGO_PORT}}/{{.MONGO_INITDB_DATABASE}}?authSource=admin",
					Secret:   true,
				},
				"MONGODB_URL": {
					Kind:     EnvKindComputedURL,
					Template: "mongodb://{{.MONGO_INITDB_ROOT_USERNAME}}:{{.MONGO_INITDB_ROOT_PASSWORD}}@{{.MIDNIGHT_PRIVATE_DOMAIN}}:{{.MONGO_PORT}}/{{.MONGO_INITDB_DATABASE}}?authSource=admin",
					Secret:   true,
				},
			},
			Volume: &TemplateVolume{Name: "data", MountPath: "/data/db", SizeBytes: 1 << 30},
			Healthcheck: &TemplateHealthcheck{
				Cmd: `mongosh --quiet --nodb --eval 'const c=connect(process.env.MONGODB_URL); quit(c.runCommand({ping:1}).ok === 1 ? 0 : 2)'`,
			},
			Ports: []TemplatePort{{Port: 27017, Protocol: "tcp"}},
		}},
	}
}

// Summary is a compact metadata view used by the REST list endpoint.
type Summary struct {
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags,omitempty"`
}

// Summaries returns one Summary per built-in template.
func Summaries() []Summary {
	tmpls := Builtins()
	out := make([]Summary, 0, len(tmpls))
	for _, tmpl := range tmpls {
		out = append(out, Summary{
			Slug:    tmpl.Slug,
			Name:    tmpl.Name,
			Version: tmpl.Version,
			Summary: tmpl.Summary,
			Tags:    append([]string(nil), tmpl.Tags...),
		})
	}
	return out
}
