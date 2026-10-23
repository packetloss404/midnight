package templates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type TrustClass string
type DatabaseEngine string
type RedisMode string

const (
	TrustClassOfficial TrustClass = "official"

	DatabaseEnginePostgres DatabaseEngine = "postgres"
	DatabaseEngineRedis    DatabaseEngine = "redis"
	DatabaseEngineMySQL    DatabaseEngine = "mysql"
	DatabaseEngineMongoDB  DatabaseEngine = "mongodb"

	RedisModeCache   RedisMode = "cache"
	RedisModeDurable RedisMode = "durable"
)

var requiredSupportGates = []string{"backup", "restore", "upgrade", "uid-gid", "smoke-test", "support-matrix"}

type DatabaseTemplate struct {
	ID                     string
	Engine                 DatabaseEngine
	EngineVersion          string
	TrustClass             TrustClass
	Image                  string
	ImageDigest            string
	MountPath              string
	InternalPort           int
	Healthcheck            string
	ConnectionURLFormat    string
	BackupCommand          string
	RestoreCommand         string
	SmokeTestCommand       string
	ExpectedUID            int
	ExpectedGID            int
	SupportedVersions      []string
	RestoreCompatibleWith  []string
	BackupPolicy           string
	TCPProxyDefaultEnabled bool
	UnsupportedModes       []string
	SupportGates           map[string]bool
	RedisMode              RedisMode
}

func OfficialDatabaseTemplates() []DatabaseTemplate {
	templates := []DatabaseTemplate{
		postgresTemplate(),
		redisCacheTemplate(),
		redisDurableTemplate(),
		mysqlTemplate(),
		mongoSingleNodeTemplate(),
	}
	sort.SliceStable(templates, func(i, j int) bool { return templates[i].ID < templates[j].ID })
	return templates
}

func ValidateOfficialDatabaseTemplate(t DatabaseTemplate) error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("template id is required")
	}
	if t.TrustClass != TrustClassOfficial {
		return fmt.Errorf("template %q trust class = %q, want official", t.ID, t.TrustClass)
	}
	if t.Engine == "" || t.EngineVersion == "" || t.Image == "" || t.ImageDigest == "" {
		return fmt.Errorf("template %q must declare engine, engine version, image, and image digest", t.ID)
	}
	if !digestPattern.MatchString(t.ImageDigest) || !strings.Contains(t.Image, "@"+t.ImageDigest) {
		return fmt.Errorf("template %q image must be pinned by sha256 digest", t.ID)
	}
	if t.MountPath == "" || t.InternalPort <= 0 || t.Healthcheck == "" || t.ConnectionURLFormat == "" {
		return fmt.Errorf("template %q must declare mount path, internal port, healthcheck, and connection URL format", t.ID)
	}
	if t.BackupCommand == "" || t.RestoreCommand == "" || t.SmokeTestCommand == "" || t.BackupPolicy == "" {
		return fmt.Errorf("template %q must declare backup, restore, smoke-test, and backup policy", t.ID)
	}
	if t.ExpectedUID < 0 || t.ExpectedGID < 0 {
		return fmt.Errorf("template %q expected UID/GID must be non-negative", t.ID)
	}
	if len(t.SupportedVersions) == 0 || len(t.RestoreCompatibleWith) == 0 {
		return fmt.Errorf("template %q must declare supported and restore-compatible versions", t.ID)
	}
	if t.TCPProxyDefaultEnabled {
		return fmt.Errorf("template %q must not enable TCP proxy by default", t.ID)
	}
	for _, gate := range requiredSupportGates {
		if !t.SupportGates[gate] {
			return fmt.Errorf("template %q support gate %q has not passed", t.ID, gate)
		}
	}
	if t.Engine == DatabaseEngineRedis && t.RedisMode != RedisModeCache && t.RedisMode != RedisModeDurable {
		return fmt.Errorf("template %q redis mode must be cache or durable", t.ID)
	}
	if t.Engine == DatabaseEngineMongoDB && !contains(t.UnsupportedModes, "replica-set") {
		return fmt.Errorf("template %q must reject MongoDB replica-set mode", t.ID)
	}
	if t.Engine == DatabaseEngineMongoDB && !contains(t.UnsupportedModes, "sharded-cluster") {
		return fmt.Errorf("template %q must reject MongoDB sharded-cluster mode", t.ID)
	}
	return nil
}

func RequiredSupportGates() []string {
	return append([]string(nil), requiredSupportGates...)
}

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func passedGates() map[string]bool {
	gates := make(map[string]bool, len(requiredSupportGates))
	for _, gate := range requiredSupportGates {
		gates[gate] = true
	}
	return gates
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func postgresTemplate() DatabaseTemplate {
	return DatabaseTemplate{
		ID:                    "postgres-16",
		Engine:                DatabaseEnginePostgres,
		EngineVersion:         "16",
		TrustClass:            TrustClassOfficial,
		Image:                 "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20",
		ImageDigest:           "sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20",
		MountPath:             "/var/lib/postgresql/data",
		InternalPort:          5432,
		Healthcheck:           "pg_isready -U $POSTGRES_USER -d $POSTGRES_DB",
		ConnectionURLFormat:   "postgres://${HOST}:${PORT}/${DATABASE}?user=${USER}",
		BackupCommand:         "pg_dumpall --globals-only && pg_dump --format=custom --blobs --no-owner",
		RestoreCommand:        "pg_restore --clean --if-exists --no-owner",
		SmokeTestCommand:      "psql ${DATABASE_URL} -c 'select 1'",
		ExpectedUID:           999,
		ExpectedGID:           999,
		SupportedVersions:     []string{"16"},
		RestoreCompatibleWith: []string{"16"},
		BackupPolicy:          "daily logical backup, 7-day retention",
		UnsupportedModes:      []string{"in-place-major-upgrade"},
		SupportGates:          passedGates(),
	}
}

func redisCacheTemplate() DatabaseTemplate {
	return redisTemplate("redis-7-cache", RedisModeCache, "no durability promise; persistence disabled")
}

func redisDurableTemplate() DatabaseTemplate {
	return redisTemplate("redis-7-durable", RedisModeDurable, "daily RDB/AOF-coordinated backup, 7-day retention")
}

func redisTemplate(id string, mode RedisMode, policy string) DatabaseTemplate {
	return DatabaseTemplate{
		ID:                    id,
		Engine:                DatabaseEngineRedis,
		EngineVersion:         "7",
		TrustClass:            TrustClassOfficial,
		Image:                 "redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e",
		ImageDigest:           "sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e",
		MountPath:             "/data",
		InternalPort:          6379,
		Healthcheck:           `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping`,
		ConnectionURLFormat:   "redis://:${PASSWORD}@${HOST}:${PORT}/0",
		BackupCommand:         "redis-cli BGSAVE and wait for rdb_last_bgsave_status=ok",
		RestoreCommand:        "restore dump.rdb into replacement volume before start",
		SmokeTestCommand:      `REDISCLI_AUTH="${PASSWORD}" redis-cli -h "${HOST}" -p "${PORT}" ping`,
		ExpectedUID:           999,
		ExpectedGID:           999,
		SupportedVersions:     []string{"7"},
		RestoreCompatibleWith: []string{"7"},
		BackupPolicy:          policy,
		UnsupportedModes:      []string{"cluster", "sentinel"},
		SupportGates:          passedGates(),
		RedisMode:             mode,
	}
}

func mysqlTemplate() DatabaseTemplate {
	return DatabaseTemplate{
		ID:                    "mysql-8",
		Engine:                DatabaseEngineMySQL,
		EngineVersion:         "8.4",
		TrustClass:            TrustClassOfficial,
		Image:                 "mysql:8.4.0@sha256:dab7049abafe3a0e12cbe5e49050cf149881c0cd9665c289e5808b9dad39c9e0",
		ImageDigest:           "sha256:dab7049abafe3a0e12cbe5e49050cf149881c0cd9665c289e5808b9dad39c9e0",
		MountPath:             "/var/lib/mysql",
		InternalPort:          3306,
		Healthcheck:           `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqladmin ping -h 127.0.0.1 -u root`,
		ConnectionURLFormat:   "mysql://${USER}:${PASSWORD}@${HOST}:${PORT}/${DATABASE}",
		BackupCommand:         "mysqldump --single-transaction --routines --triggers --all-databases",
		RestoreCommand:        "mysql < logical-dump.sql into replacement volume",
		SmokeTestCommand:      `MYSQL_PWD="${PASSWORD}" mysql --protocol=TCP --host="${HOST}" --port="${PORT}" --user="${USER}" "${DATABASE}" -e 'select 1'`,
		ExpectedUID:           999,
		ExpectedGID:           999,
		SupportedVersions:     []string{"8.4"},
		RestoreCompatibleWith: []string{"8.4"},
		BackupPolicy:          "daily logical backup, 7-day retention",
		UnsupportedModes:      []string{"group-replication", "in-place-major-upgrade"},
		SupportGates:          passedGates(),
	}
}

func mongoSingleNodeTemplate() DatabaseTemplate {
	return DatabaseTemplate{
		ID:                    "mongodb-7-single-node",
		Engine:                DatabaseEngineMongoDB,
		EngineVersion:         "7",
		TrustClass:            TrustClassOfficial,
		Image:                 "mongo:7.0.9@sha256:97aac78a80553735b3d9b9b7212803468781b4859645f892a3d04e6b621a7b77",
		ImageDigest:           "sha256:97aac78a80553735b3d9b9b7212803468781b4859645f892a3d04e6b621a7b77",
		MountPath:             "/data/db",
		InternalPort:          27017,
		Healthcheck:           `mongosh --quiet --nodb --eval 'const c=connect(process.env.MONGODB_URL); quit(c.runCommand({ping:1}).ok === 1 ? 0 : 2)'`,
		ConnectionURLFormat:   "mongodb://${USER}:${PASSWORD}@${HOST}:${PORT}/${DATABASE}?authSource=admin",
		BackupCommand:         "mongodump --archive --gzip",
		RestoreCommand:        "mongorestore --archive --gzip into replacement volume",
		SmokeTestCommand:      `mongosh --quiet --nodb --eval 'const c=connect(process.env.MONGODB_URL); quit(c.runCommand({ping:1}).ok === 1 ? 0 : 2)'`,
		ExpectedUID:           999,
		ExpectedGID:           999,
		SupportedVersions:     []string{"7"},
		RestoreCompatibleWith: []string{"7"},
		BackupPolicy:          "daily mongodump backup, 7-day retention",
		UnsupportedModes:      []string{"replica-set", "sharded-cluster"},
		SupportGates:          passedGates(),
	}
}
