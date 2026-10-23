#!/usr/bin/env bash
#
# doctor.sh - health checks for the Midnight local dev stack.
#
# Portable bash re-implementation of the reference stack's doctor.ps1. It runs a
# set of STATIC checks (no running containers required) and, with --live, a set
# of LIVE checks against the running stack.
#
# Usage:
#   scripts/dev/doctor.sh              # static checks only
#   scripts/dev/doctor.sh --live       # static + live checks (stack must be up)
#
# Env overrides:
#   COMPOSE_FILE           compose file (default: docker-compose.local.yml)
#   MIDNIGHT_API_PORT      default 8080
#   MIDNIGHT_POSTGRES_PORT default 15432
#   MIDNIGHT_REGISTRY_PORT default 15000
#   MIDNIGHT_BUILDKIT_PORT default 11234
#   MIDNIGHT_TRAEFIK_HTTP_PORT  default 8081
#   MIDNIGHT_TRAEFIK_HTTPS_PORT default 8443
#   MIDNIGHT_POSTGRES_DB / _USER  used for the live psql migration check
set -u

# --- locate repo root (parent of scripts/) ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${REPO_ROOT}" || {
  echo "doctor: cannot enter repository root: ${REPO_ROOT}" >&2
  exit 1
}

LIVE=0
for arg in "$@"; do
  case "${arg}" in
    --live) LIVE=1 ;;
    --skip-live) LIVE=0 ;;
    -h|--help)
      sed -n '2,/^set -u$/p' "${BASH_SOURCE[0]}" |
        sed -n 's/^# \{0,1\}//p'
      exit 0
      ;;
    *) echo "doctor: unknown argument: ${arg}" >&2; exit 64 ;;
  esac
done

COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.local.yml}"
HOST="127.0.0.1"
API_PORT="${MIDNIGHT_API_PORT:-8080}"
POSTGRES_PORT="${MIDNIGHT_POSTGRES_PORT:-15432}"
REGISTRY_PORT="${MIDNIGHT_REGISTRY_PORT:-15000}"
BUILDKIT_PORT="${MIDNIGHT_BUILDKIT_PORT:-11234}"
TRAEFIK_HTTP_PORT="${MIDNIGHT_TRAEFIK_HTTP_PORT:-8081}"
TRAEFIK_HTTPS_PORT="${MIDNIGHT_TRAEFIK_HTTPS_PORT:-8443}"
PG_DB="${MIDNIGHT_POSTGRES_DB:-midnight}"
PG_USER="${MIDNIGHT_POSTGRES_USER:-midnight}"

PASS_COUNT=0
FAIL_COUNT=0

pass() { printf 'PASS %s%s\n' "$1" "${2:+ - $2}"; PASS_COUNT=$((PASS_COUNT + 1)); }
fail() { printf 'FAIL %s%s\n' "$1" "${2:+ - $2}"; FAIL_COUNT=$((FAIL_COUNT + 1)); }
info() { printf 'INFO %s%s\n' "$1" "${2:+ - $2}"; }

have() { command -v "$1" >/dev/null 2>&1; }

# --- compose helper: resolves `docker compose` vs legacy `docker-compose` ---
COMPOSE_CMD=()
resolve_compose() {
  if have docker && docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD=(docker compose -f "${COMPOSE_FILE}")
    return 0
  fi
  if have docker-compose; then
    COMPOSE_CMD=(docker-compose -f "${COMPOSE_FILE}")
    return 0
  fi
  return 1
}

# tcp_probe HOST PORT -> 0 if a TCP connection succeeds
tcp_probe() {
  local h="$1" p="$2"
  if have nc; then
    nc -z -w 2 "${h}" "${p}" >/dev/null 2>&1 && return 0 || return 1
  fi
  # bash /dev/tcp fallback
  ( exec 3<>"/dev/tcp/${h}/${p}" ) >/dev/null 2>&1 && return 0 || return 1
}

echo "=== Midnight local stack doctor (static checks) ==="

# repo root
pass "repo root" "${REPO_ROOT}"

# compose file present
if [ -f "${COMPOSE_FILE}" ]; then
  pass "compose file" "${COMPOSE_FILE}"
else
  fail "compose file" "${COMPOSE_FILE} not found"
fi

# traefik static config present
if [ -f "config/traefik/traefik.yml" ]; then
  pass "traefik config" "config/traefik/traefik.yml"
else
  fail "traefik config" "config/traefik/traefik.yml not found"
fi

# migrations dir + count of .up.sql files
MIGRATION_FILES=0
if [ -d "migrations" ]; then
  MIGRATION_FILES="$(find migrations -maxdepth 1 -name '*.up.sql' -type f 2>/dev/null | wc -l | tr -d '[:space:]')"
  pass "migration files" "${MIGRATION_FILES} *.up.sql under migrations/"
else
  fail "migrations dir" "migrations/ not found"
fi

# docker + compose availability
if resolve_compose; then
  pass "docker compose" "$(printf '%s ' "${COMPOSE_CMD[@]}" | sed 's/ $//')"
else
  fail "docker compose" "neither 'docker compose' nor 'docker-compose' available"
fi

# discover compose services (needs a valid compose config parse)
SERVICES=""
if [ "${#COMPOSE_CMD[@]}" -gt 0 ]; then
  if SERVICES="$("${COMPOSE_CMD[@]}" config --services 2>/dev/null)"; then
    pass "compose config" "services: $(echo "${SERVICES}" | tr '\n' ' ' | sed 's/ $//')"
  else
    fail "compose config" "could not parse ${COMPOSE_FILE}"
    SERVICES=""
  fi
fi

# expected core services present in discovered set
service_present() { echo "${SERVICES}" | grep -qx "$1"; }
for svc in postgres migrate api deploy-worker build-worker registry buildkit traefik; do
  if [ -n "${SERVICES}" ]; then
    if service_present "${svc}"; then
      pass "service: ${svc}" ""
    else
      fail "service: ${svc}" "missing from compose config"
    fi
  fi
done

# local traefik state dirs (git-ignored under .midnight/)
DYNAMIC_DIR="${MIDNIGHT_TRAEFIK_DYNAMIC:-.midnight/local/traefik/dynamic}"
ACME_DIR="${MIDNIGHT_TRAEFIK_ACME:-.midnight/local/traefik/acme}"
if [ -d "${DYNAMIC_DIR}" ]; then
  pass "traefik dynamic dir" "${DYNAMIC_DIR}"
else
  info "traefik dynamic dir" "${DYNAMIC_DIR} not yet created (make dev-up creates it)"
fi
if [ -d "${ACME_DIR}" ]; then
  pass "traefik acme dir" "${ACME_DIR}"
else
  info "traefik acme dir" "${ACME_DIR} not yet created (make dev-up creates it)"
fi

if [ "${LIVE}" -eq 0 ]; then
  info "live checks" "skipped; pass --live once the stack is up"
else
  echo "=== live checks ==="

  # host port probes
  probe_port() {
    local name="$1" port="$2"
    if tcp_probe "${HOST}" "${port}"; then
      pass "port ${name}" "${HOST}:${port}"
    else
      fail "port ${name}" "${HOST}:${port}"
    fi
  }
  probe_port "api" "${API_PORT}"
  probe_port "postgres" "${POSTGRES_PORT}"
  probe_port "registry" "${REGISTRY_PORT}"
  probe_port "buildkit" "${BUILDKIT_PORT}"
  probe_port "traefik http" "${TRAEFIK_HTTP_PORT}"
  probe_port "traefik https" "${TRAEFIK_HTTPS_PORT}"

  # docker networks
  if have docker; then
    for net in midnight-control midnight-ingress; do
      if docker network inspect "${net}" >/dev/null 2>&1; then
        pass "network: ${net}" ""
      else
        fail "network: ${net}" "not found"
      fi
    done
  fi

  # applied migration count vs on-disk migration file count
  if [ "${#COMPOSE_CMD[@]}" -gt 0 ] && [ "${MIGRATION_FILES}" -gt 0 ]; then
    APPLIED="$("${COMPOSE_CMD[@]}" exec -T postgres \
      psql -U "${PG_USER}" -d "${PG_DB}" -tAc \
      'SELECT count(*) FROM schema_migrations;' 2>/dev/null | tr -d '[:space:]')"
    if [ -n "${APPLIED}" ]; then
      if [ "${APPLIED}" -ge "${MIGRATION_FILES}" ]; then
        pass "migrations applied" "${APPLIED}/${MIGRATION_FILES}"
      else
        fail "migrations applied" "${APPLIED}/${MIGRATION_FILES}"
      fi
    else
      fail "migrations applied" "could not query schema_migrations (is postgres up?)"
    fi
  fi

  # api liveness (TCP already probed; note there is no unauthenticated /health path)
  info "api note" "main exposes no unauthenticated /health; port probe above is the liveness signal"
fi

echo "=== summary: ${PASS_COUNT} passed, ${FAIL_COUNT} failed ==="
[ "${FAIL_COUNT}" -eq 0 ] || exit 1
