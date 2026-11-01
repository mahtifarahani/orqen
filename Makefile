GO ?= go

# Select the SDK belonging to the active Xcode toolchain for native Keychain builds.
ifeq ($(shell uname -s),Darwin)
export SDKROOT ?= $(shell xcrun --sdk macosx --show-sdk-path)
export MACOSX_DEPLOYMENT_TARGET ?= $(shell xcrun --sdk macosx --show-sdk-version)
endif

.PHONY: dev-mongo dev-server dev-web login-link openapi openapi-check web-build build release dist check fmt

dev-mongo:
	scripts/dev-mongo.sh start

dev-server:
	ORQEN_DEV=true ORQEN_DATA_DIR=.orqen-dev/data ORQEN_CREDENTIAL_STORE=keychain $(GO) run ./cmd/orqen serve

dev-web:
	pnpm -C web dev

login-link:
	$(GO) run ./cmd/orqen login-link

openapi:
	$(GO) run ./cmd/orqen openapi > web/openapi.json
	pnpm -C web gen:api

openapi-check:
	scripts/openapi-check.sh

web-build:
	pnpm -C web build

build: web-build
	mkdir -p bin
	$(GO) build -trimpath -o bin/orqen ./cmd/orqen

# release is the only target that sets the release build channel. Only a release binary started with
# ORQEN_ENV=prod (and without ORQEN_DEV) may write to hosts outside loopback; see docs/LIVE-TRIAL.md.
release: web-build
	mkdir -p bin
	$(GO) build -trimpath -ldflags "-X orqen/internal/extwrite.BuildChannel=release" -o bin/orqen-release ./cmd/orqen

# dist is the artefact set of a release: the release binary, SHA256SUMS and THIRD_PARTY_LICENSES.txt (all offline).
dist: release
	scripts/dist.sh

fmt:
	$(GO) fmt ./...

check:
	@test -z "$$(gofmt -l cmd internal api web/assets.go)" || (echo "Go files needed formatting" >&2; exit 1)
	$(GO) vet ./...
	$(GO) test -race -timeout 40m ./...
	pnpm -C web lint
	pnpm -C web typecheck
	pnpm -C web check:rtl
	pnpm -C web test --run
	pnpm -C web build
	$(MAKE) openapi-check
