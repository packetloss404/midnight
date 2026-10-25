RELEASE_EVIDENCE_DIR ?= artifacts/phase0
RELEASE_EVIDENCE_JSON ?= $(RELEASE_EVIDENCE_DIR)/release-evidence.json
RELEASE_EVIDENCE_INDEX_JSON ?= $(RELEASE_EVIDENCE_DIR)/release-evidence-index.json
RELEASE_SUMMARY_JSON ?= $(RELEASE_EVIDENCE_DIR)/release-summary.json
MIDNIGHT_GO_TOOLCHAIN ?= go1.26.10

.PHONY: bootstrap build test format lint supplychain audit phase0-gate phase0-drills release-check ci-local ci-local-test clean-artifacts generate generate-midnightctl generate-midnightctl-check api-contract-check api-v1-graduation-report api-v1-graduation-check adr-index db-integration package-deb-provenance-test package-deb package-deb-sign

bootstrap:
	go version
	npm --version

build:
	go build ./...

test:
	go test ./... -count=1

format:
	gofmt -w cmd internal pkg tools

# db-integration spins up a real Postgres 16 testcontainer and exercises every
# Postgres-backed store added by Phase 1 / Lane 1 (DATA). Requires Docker.
db-integration:
	go test -tags=integration -count=1 -timeout=10m ./internal/db/...

lint:
	test -z "$$(gofmt -l cmd internal pkg tools)"
	go vet ./...
	go run ./tools/lint

supplychain:
	@echo "supplychain: local dependency inventory plus npm production audits"
	go list ./...
	npm audit --omit=dev --package-lock-only --prefix web-next

audit: supplychain

phase0-gate: lint test build audit phase0-drills

phase0-drills:
	go run ./tools/phase0-gate --format=json --execute-drills

release-check:
	mkdir -p $(RELEASE_EVIDENCE_DIR)
	go test ./...
	go vet ./...
	go build ./...
	go run ./tools/lint
	go run ./tools/contract-tests
	scripts/generate-midnightctl.sh check
	go run ./tools/api-client-contract --check
	go run ./tools/contract-tests/schemathesis
	go run ./tools/api-contract-views --stable-output api/openapi/v1/midnight.yaml --check
	go run ./tools/api-v1-graduation --format=json --require-ready
	go test ./internal/cloudevents -run TestAsyncAPIEventCatalogMatchesRuntime -count=1
	go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence $(RELEASE_EVIDENCE_JSON) --release-evidence-index $(RELEASE_EVIDENCE_INDEX_JSON) --verify-release-evidence-index $(RELEASE_EVIDENCE_INDEX_JSON) --verify-evidence-files --release-summary $(RELEASE_SUMMARY_JSON)
	go run ./tools/phase0-gate --format=json --verify-release-evidence-index $(RELEASE_EVIDENCE_INDEX_JSON) --verify-evidence-files
	packaging/release/test-debian-package-provenance.sh
	npm --prefix web-next ci
	npm --prefix web-next test
	npm --prefix web-next run typecheck
	npm --prefix web-next run build
	npm audit --omit=dev --package-lock-only --prefix web-next

# Provider-independent full CI. This is the release fallback when hosted
# runners are unavailable; it writes commit-bound evidence under artifacts/.
ci-local:
	scripts/ci/local-gate.sh

ci-local-test:
	scripts/ci/local-gate_test.sh

clean-artifacts:
	test "$(RELEASE_EVIDENCE_DIR)" = "artifacts/phase0"
	mkdir -p $(RELEASE_EVIDENCE_DIR)
	find $(RELEASE_EVIDENCE_DIR) -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +

generate:
	@echo "generate: running go generate hooks declared in source files"
	go generate ./...

generate-midnightctl:
	scripts/generate-midnightctl.sh generate

generate-midnightctl-check:
	scripts/generate-midnightctl.sh check

api-contract-check:
	go run ./tools/api-contract-metadata --check
	go run ./tools/contract-tests
	go run ./tools/api-client-contract --check
	go test ./pkg/midnightctl ./tools/api-client-contract ./tools/contract-tests/schemathesis -count=1
	go run ./tools/contract-tests/schemathesis
	go run ./tools/api-contract-views --stable-output api/openapi/v1/midnight.yaml --check
	go test ./internal/api -run 'Test(OpenAPIRouteContract|ProductionRuntimeRoutesAreDocumented)$$' -count=1
	go test ./internal/cloudevents -run TestAsyncAPIEventCatalogMatchesRuntime -count=1

api-v1-graduation-report:
	go run ./tools/api-v1-graduation --format=json

api-v1-graduation-check:
	go run ./tools/api-v1-graduation --format=json --require-ready

adr-index:
	test -f docs/adr/README.md

# package-deb builds the unsigned Debian binary artifact set. Requires a
# Debian / Ubuntu build host with debhelper-13 and a pristine detached
# worktree at the reviewed source commit. Ambient VITE_* values and ignored
# web-next/.env inputs are refused before the build starts.
# Produces the installable .deb, host-native dbgsym (.deb on Debian or .ddeb
# on Ubuntu), .changes, and .buildinfo under ./build/deb/.
# Skip-version upgrades are blocked by the Depends/Breaks fields on
# debian/control.
package-deb-provenance-test:
	packaging/release/test-debian-package-provenance.sh

package-deb: package-deb-provenance-test
	mkdir -p build/deb
	@if ! command -v dpkg-buildpackage >/dev/null 2>&1; then \
	    echo "package-deb: dpkg-buildpackage not in PATH (run on a Debian/Ubuntu host or use the manual release lane)" >&2; \
	    exit 2; \
	fi
	@if [ -e debian ]; then \
	    echo "package-deb: refusing to overwrite existing ./debian directory" >&2; \
	    exit 2; \
	fi
	@set -e; \
	    trap 'rm -rf debian' EXIT; \
	    eval "$$(packaging/release/debian-build-metadata.sh .)"; \
	    package_arch="$$(dpkg-architecture -qDEB_HOST_ARCH)"; \
	    cp -R deploy/debian debian; \
	    DEB_BUILD_OPTIONS=nocheck \
	    GOTOOLCHAIN=$(MIDNIGHT_GO_TOOLCHAIN) \
	    GO=$$(command -v go) \
	    MIDNIGHT_VERSION="$$MIDNIGHT_PRODUCT_VERSION" \
	    MIDNIGHT_COMMIT="$$MIDNIGHT_SOURCE_COMMIT" \
	    MIDNIGHT_DATE="$$MIDNIGHT_SOURCE_DATE" \
	    SOURCE_DATE_EPOCH="$$MIDNIGHT_SOURCE_EPOCH" \
	      dpkg-buildpackage -d -us -uc -b; \
	    packaging/release/collect-debian-artifacts.sh \
	      .. build/deb "$$MIDNIGHT_DEBIAN_VERSION" "$$package_arch"; \
	    packaging/release/verify-debian-artifacts.sh \
	      build/deb "$$MIDNIGHT_DEBIAN_VERSION" "$$MIDNIGHT_PRODUCT_VERSION" \
	      "$$MIDNIGHT_SOURCE_COMMIT" "$$MIDNIGHT_SOURCE_DATE" "$$MIDNIGHT_SOURCE_EPOCH" \
	      $(MIDNIGHT_GO_TOOLCHAIN) "$$package_arch"
	@echo "package-deb: produced complete Debian artifact set in build/deb/"

# package-deb-sign builds AND signs the .deb using the operator's
# GPG key (env MIDNIGHT_RELEASE_GPG_KEYID). The signing key custody
# story is documented in agile/handoff/governance/RELEASE_KEY_CUSTODY.md
# (Agent 4 deliverable). Manual release workflows use the secret
# COSIGN_KEYID + APT_KEY_ID pair; the local target requires the operator to
# source those.
package-deb-sign: package-deb-provenance-test
	@if [ -z "$(MIDNIGHT_RELEASE_GPG_KEYID)" ]; then \
	    echo "package-deb-sign: set MIDNIGHT_RELEASE_GPG_KEYID to the signing key id" >&2; \
	    exit 2; \
	fi
	mkdir -p build/deb
	@if [ -e debian ]; then \
	    echo "package-deb-sign: refusing to overwrite existing ./debian directory" >&2; \
	    exit 2; \
	fi
	@set -e; \
	    trap 'rm -rf debian' EXIT; \
	    eval "$$(packaging/release/debian-build-metadata.sh .)"; \
	    package_arch="$$(dpkg-architecture -qDEB_HOST_ARCH)"; \
	    cp -R deploy/debian debian; \
	    DEB_BUILD_OPTIONS=nocheck \
	    GOTOOLCHAIN=$(MIDNIGHT_GO_TOOLCHAIN) \
	    GO=$$(command -v go) \
	    MIDNIGHT_VERSION="$$MIDNIGHT_PRODUCT_VERSION" \
	    MIDNIGHT_COMMIT="$$MIDNIGHT_SOURCE_COMMIT" \
	    MIDNIGHT_DATE="$$MIDNIGHT_SOURCE_DATE" \
	    SOURCE_DATE_EPOCH="$$MIDNIGHT_SOURCE_EPOCH" \
	      dpkg-buildpackage -d --sign-key=$(MIDNIGHT_RELEASE_GPG_KEYID) -b; \
	    packaging/release/collect-debian-artifacts.sh \
	      .. build/deb "$$MIDNIGHT_DEBIAN_VERSION" "$$package_arch"; \
	    packaging/release/verify-debian-artifacts.sh \
	      build/deb "$$MIDNIGHT_DEBIAN_VERSION" "$$MIDNIGHT_PRODUCT_VERSION" \
	      "$$MIDNIGHT_SOURCE_COMMIT" "$$MIDNIGHT_SOURCE_DATE" "$$MIDNIGHT_SOURCE_EPOCH" \
	      $(MIDNIGHT_GO_TOOLCHAIN) "$$package_arch"
	@echo "package-deb-sign: produced signed Debian artifact set in build/deb/"

# ---------------------------------------------------------------------------
# Local dev stack (docker-compose.local.yml). See docs/dev-local-stack.md.
# ---------------------------------------------------------------------------
DEV_COMPOSE_FILE ?= docker-compose.local.yml

.PHONY: dev-up dev-down dev-logs dev-doctor

# dev-up creates git-ignored traefik state dirs, then brings the stack up.
dev-up:
	mkdir -p .midnight/local/traefik/dynamic .midnight/local/traefik/acme
	docker compose -f $(DEV_COMPOSE_FILE) up -d --build

dev-down:
	docker compose -f $(DEV_COMPOSE_FILE) down

dev-logs:
	docker compose -f $(DEV_COMPOSE_FILE) logs -f

# dev-doctor runs static checks; pass ARGS=--live once the stack is up.
dev-doctor:
	bash scripts/dev/doctor.sh $(ARGS)

.PHONY: web-build web-embed build-server

WEB_EMBED_DIR := internal/api/webdist

# web-build builds the web-next dashboard into web-next/dist. Requires Node.
web-build:
	cd web-next && npm ci && npm run build

# web-embed stages web-next/dist into internal/api/webdist, which
# internal/api/webui_embed.go compiles into midnight-server with go:embed.
# Run it before any `go build` that produces a shippable server binary.
web-embed: web-build
	@test -f web-next/dist/index.html || { \
	  echo "ERROR: web-next/dist/index.html is missing; the console build did not produce a bundle" >&2; \
	  exit 1; \
	}
	find $(WEB_EMBED_DIR) -mindepth 1 -maxdepth 1 ! -name .gitignore -exec rm -rf -- {} +
	cp -R web-next/dist/. $(WEB_EMBED_DIR)/
	@test -f $(WEB_EMBED_DIR)/index.html

# build-server produces the self-contained server binary: one artifact that
# serves its own console. It refuses to run the Go compiler when the console
# has not been staged, so a console-less binary is a loud build failure rather
# than a server that 503s at "/".
build-server:
	@test -f $(WEB_EMBED_DIR)/index.html || { \
	  echo "ERROR: $(WEB_EMBED_DIR)/index.html is missing; run 'make web-embed' before building midnight-server" >&2; \
	  exit 1; \
	}
	go build -trimpath -o build/midnight-server ./cmd/midnight-server
