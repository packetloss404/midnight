package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/domain"
	"github.com/midnight/midnight/internal/failurecodes"
)

const instanceDomainsPath = v1alphaPrefix + "/instance/domains"

var (
	ErrInstanceDomainsNotConfigured = errors.New("instance domains are not configured")
	ErrInstanceDomainsConflict      = errors.New("instance domains changed concurrently")
)

type InstanceDomainSettings struct {
	RootDomain       string    `json:"root_domain"`
	ConsoleHostname  string    `json:"console_hostname"`
	AppsDomain       string    `json:"apps_domain"`
	IngressTarget    string    `json:"ingress_target"`
	ActiveAppsDomain string    `json:"active_apps_domain,omitempty"`
	Version          int64     `json:"version"`
	UpdatedAt        time.Time `json:"updated_at"`
	RestartRequired  bool      `json:"restart_required"`
}

type InstanceDomainSettingsStore interface {
	Get(context.Context) (InstanceDomainSettings, error)
	Put(context.Context, InstanceDomainSettings, int64) (InstanceDomainSettings, error)
}

type PostgresInstanceDomainSettingsStore struct {
	Pool *pg.Pool
}

func (s *PostgresInstanceDomainSettingsStore) Get(ctx context.Context) (InstanceDomainSettings, error) {
	if s == nil || s.Pool == nil {
		return InstanceDomainSettings{}, errors.New("instance domain settings store is unavailable")
	}
	var value InstanceDomainSettings
	err := s.Pool.QueryRow(ctx, `
		SELECT root_domain,console_hostname,apps_domain,ingress_target,resource_version,updated_at
		  FROM instance_domain_settings
		 WHERE singleton=TRUE`,
	).Scan(&value.RootDomain, &value.ConsoleHostname, &value.AppsDomain, &value.IngressTarget, &value.Version, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return InstanceDomainSettings{}, ErrInstanceDomainsNotConfigured
	}
	if err != nil {
		return InstanceDomainSettings{}, fmt.Errorf("read instance domain settings: %w", err)
	}
	return value, nil
}

func (s *PostgresInstanceDomainSettingsStore) Put(ctx context.Context, value InstanceDomainSettings, expectedVersion int64) (InstanceDomainSettings, error) {
	if s == nil || s.Pool == nil {
		return InstanceDomainSettings{}, errors.New("instance domain settings store is unavailable")
	}
	var out InstanceDomainSettings
	if expectedVersion == 0 {
		err := s.Pool.QueryRow(ctx, `
			INSERT INTO instance_domain_settings(singleton,root_domain,console_hostname,apps_domain,ingress_target,resource_version,updated_at)
			VALUES (TRUE,$1,$2,$3,$4,1,CURRENT_TIMESTAMP)
			ON CONFLICT (singleton) DO NOTHING
			RETURNING root_domain,console_hostname,apps_domain,ingress_target,resource_version,updated_at`,
			value.RootDomain, value.ConsoleHostname, value.AppsDomain, value.IngressTarget,
		).Scan(&out.RootDomain, &out.ConsoleHostname, &out.AppsDomain, &out.IngressTarget, &out.Version, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return InstanceDomainSettings{}, ErrInstanceDomainsConflict
		}
		if err != nil {
			return InstanceDomainSettings{}, fmt.Errorf("create instance domain settings: %w", err)
		}
		return out, nil
	}
	err := s.Pool.QueryRow(ctx, `
		UPDATE instance_domain_settings
		   SET root_domain=$1,console_hostname=$2,apps_domain=$3,ingress_target=$4,
		       resource_version=resource_version+1,updated_at=CURRENT_TIMESTAMP
		 WHERE singleton=TRUE AND resource_version=$5
		 RETURNING root_domain,console_hostname,apps_domain,ingress_target,resource_version,updated_at`,
		value.RootDomain, value.ConsoleHostname, value.AppsDomain, value.IngressTarget, expectedVersion,
	).Scan(&out.RootDomain, &out.ConsoleHostname, &out.AppsDomain, &out.IngressTarget, &out.Version, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return InstanceDomainSettings{}, ErrInstanceDomainsConflict
	}
	if err != nil {
		return InstanceDomainSettings{}, fmt.Errorf("update instance domain settings: %w", err)
	}
	return out, nil
}

func normalizeInstanceRootDomain(raw string) (InstanceDomainSettings, error) {
	root, err := domain.NormalizeHostname(strings.TrimSpace(raw))
	if err != nil {
		return InstanceDomainSettings{}, err
	}
	if strings.Count(root, ".") < 1 {
		return InstanceDomainSettings{}, errors.New("use a registrable domain such as example.com")
	}
	return InstanceDomainSettings{
		RootDomain:      root,
		ConsoleHostname: "midnight." + root,
		AppsDomain:      "apps." + root,
	}, nil
}

func inferInstanceRootDomain(appsDomain string) string {
	appsDomain = strings.Trim(strings.ToLower(strings.TrimSpace(appsDomain)), ".")
	if strings.HasPrefix(appsDomain, "apps.") {
		return strings.TrimPrefix(appsDomain, "apps.")
	}
	return appsDomain
}

func seedInstanceDomainSettings(ctx context.Context, store InstanceDomainSettingsStore, appsDomain string) (InstanceDomainSettings, error) {
	current, err := store.Get(ctx)
	if err == nil {
		return current, nil
	}
	if !errors.Is(err, ErrInstanceDomainsNotConfigured) {
		return InstanceDomainSettings{}, err
	}
	if strings.TrimSpace(appsDomain) == "" {
		return InstanceDomainSettings{}, ErrInstanceDomainsNotConfigured
	}
	value, err := normalizeInstanceRootDomain(inferInstanceRootDomain(appsDomain))
	if err != nil {
		return InstanceDomainSettings{}, fmt.Errorf("infer instance root domain: %w", err)
	}
	value.AppsDomain, err = domain.NormalizeHostname(appsDomain)
	if err != nil {
		return InstanceDomainSettings{}, err
	}
	return store.Put(ctx, value, 0)
}

type putInstanceDomainsRequest struct {
	RootDomain      string `json:"root_domain"`
	IngressTarget   string `json:"ingress_target"`
	ExpectedVersion int64  `json:"expected_version"`
}

func normalizeIngressTarget(raw string) (string, error) {
	target := strings.Trim(strings.ToLower(strings.TrimSpace(raw)), ".")
	if target == "" {
		return "", errors.New("public IP or hostname is required")
	}
	if ip := net.ParseIP(target); ip != nil {
		return ip.String(), nil
	}
	return domain.NormalizeHostname(target)
}

func instanceDomainsHandler(store InstanceDomainSettingsStore, activeAppsDomain string, emitter *auditEmitter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable instance domain settings are not configured", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			value, err := store.Get(r.Context())
			if errors.Is(err, ErrInstanceDomainsNotConfigured) {
				w.Header().Set("ETag", strongVersionETag(0))
				WriteJSON(w, http.StatusOK, map[string]any{
					"configured":         false,
					"active_apps_domain": strings.TrimSpace(activeAppsDomain),
				})
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "instance domain settings are unavailable", nil)
				return
			}
			w.Header().Set("ETag", strongVersionETag(value.Version))
			writeInstanceDomainSettings(w, value, activeAppsDomain)
		case http.MethodPut:
			var request putInstanceDomainsRequest
			if !decodeStrictJSONRequest(w, r, &request, "invalid instance domain settings") {
				return
			}
			if request.ExpectedVersion < 0 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected version cannot be negative", nil)
				return
			}
			if !requireMatchingExpectedVersion(w, r, request.ExpectedVersion) {
				return
			}
			value, err := normalizeInstanceRootDomain(request.RootDomain)
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "root domain is invalid", map[string]any{"field": "root_domain", "reason": err.Error()})
				return
			}
			value.IngressTarget, err = normalizeIngressTarget(request.IngressTarget)
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "ingress target is invalid", map[string]any{"field": "ingress_target", "reason": err.Error()})
				return
			}
			before, _ := store.Get(r.Context())
			value, err = store.Put(r.Context(), value, request.ExpectedVersion)
			if errors.Is(err, ErrInstanceDomainsConflict) {
				writeRequestError(w, r, http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "instance domain settings changed; reload and try again", nil)
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "instance domain settings could not be saved", nil)
				return
			}
			emitter.emit(r.Context(), audit.Row{
				ResourceKind:   "instance_domain_settings",
				ResourceID:     "primary",
				Action:         "instance.domains.put",
				Before:         map[string]any{"root_domain": before.RootDomain, "apps_domain": before.AppsDomain, "ingress_target": before.IngressTarget},
				After:          map[string]any{"root_domain": value.RootDomain, "apps_domain": value.AppsDomain, "ingress_target": value.IngressTarget},
				PolicyDecision: "allowed",
			})
			w.Header().Set("ETag", strongVersionETag(value.Version))
			writeInstanceDomainSettings(w, value, activeAppsDomain)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func writeInstanceDomainSettings(w http.ResponseWriter, value InstanceDomainSettings, activeAppsDomain string) {
	value.ActiveAppsDomain = strings.TrimSpace(activeAppsDomain)
	value.RestartRequired = value.ActiveAppsDomain != value.AppsDomain
	WriteJSON(w, http.StatusOK, struct {
		Configured bool `json:"configured"`
		InstanceDomainSettings
	}{Configured: true, InstanceDomainSettings: value})
}
