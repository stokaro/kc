# Developer shortcuts. The lint tools are pinned in the go.mod tool block and
# run through `go tool`, so no separate install step is needed.

GOLANGCI_LINT := go tool golangci-lint
PYTHON ?= python3
PACKAGES ?=
GO_TEST_FLAGS ?=
TEST_TIMEOUT ?= 5m
FUZZ_TIME ?= 10s
BASE ?= origin/main
CHECK_MODE ?= unit
TEST_PACKAGES = $(if $(PACKAGES),$(PACKAGES),./...)
RACE_PACKAGES = $(if $(PACKAGES),$(PACKAGES),./internal/cluster ./internal/tablecache ./internal/logmux ./internal/logresume ./internal/ui/...)
TUI_PACKAGES = $(if $(PACKAGES),$(PACKAGES),./internal/ui/... ./internal/table ./cmd/bubbleheadless)

.PHONY: all build test test-unit test-integration test-race test-tui test-headless test-fuzz test-changed test-plan check-agents vet lint lint-golangci lint-nolintguard lint-fix

all: build test lint check-agents

build:
	go build ./...

vet:
	go vet ./...

# Keep the complete check explicit: unavailable envtest must not look green.
test: test-integration

test-unit:
	KC_REQUIRE_ENVTEST= KC_SKIP_ENVTEST=1 go test -timeout $(TEST_TIMEOUT) $(GO_TEST_FLAGS) $(TEST_PACKAGES)

test-integration:
	KC_SKIP_ENVTEST= KC_REQUIRE_ENVTEST=1 go test -timeout $(TEST_TIMEOUT) $(GO_TEST_FLAGS) $(TEST_PACKAGES)

# These packages own caches, stream lifetimes and shared UI state. The regular
# integration job covers real API semantics; this pass focuses on Go races.
test-race:
	KC_REQUIRE_ENVTEST= KC_SKIP_ENVTEST=1 go test -race -timeout $(TEST_TIMEOUT) $(GO_TEST_FLAGS) $(RACE_PACKAGES)

test-tui:
	KC_REQUIRE_ENVTEST= KC_SKIP_ENVTEST=1 go test -timeout $(TEST_TIMEOUT) $(GO_TEST_FLAGS) $(TUI_PACKAGES)

# Build once outside the isolated child's home; the harness supplies its own
# envtest kubeconfig, waits for visible states and checks the real exit status.
test-headless:
	@set -eu; \
	headless_dir=$$(mktemp -d); \
	trap 'rm -rf "$$headless_dir"' EXIT; \
	go build -o "$$headless_dir/kc" ./cmd/kc; \
	go build -o "$$headless_dir/bubbleheadless" ./cmd/bubbleheadless; \
	KC_HEADLESS=1 KC_HEADLESS_KC_BINARY="$$headless_dir/kc" \
		KC_HEADLESS_DRIVER_BINARY="$$headless_dir/bubbleheadless" \
		KC_SKIP_ENVTEST= KC_REQUIRE_ENVTEST=1 \
		go test -count=1 -timeout $(TEST_TIMEOUT) $(GO_TEST_FLAGS) ./internal/testing/headless

test-fuzz:
	KC_REQUIRE_ENVTEST= KC_SKIP_ENVTEST=1 go test -run '^$$' -fuzz '^FuzzFormatted$$' -fuzztime $(FUZZ_TIME) -timeout $(TEST_TIMEOUT) ./internal/logview

test-plan:
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) scripts/test-changed.py --plan --base $(BASE) --mode $(CHECK_MODE)

test-changed:
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) scripts/test-changed.py --base $(BASE) --mode $(CHECK_MODE) --timeout $(TEST_TIMEOUT)

check-agents:
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) scripts/check-agents.py
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m unittest discover -s scripts/tests -p 'test_*.py'

lint: lint-nolintguard lint-golangci

# Every //nolint, #nosec, //gosec: and //revive: directive must name what it
# silences and say why. nolintguard is not a golangci-lint plugin, so it runs
# through go vet's analyzer driver. It reads the whole tree, not only new code.
lint-nolintguard:
	@echo "Running nolintguard..."
	@nolintguard="$$(go tool -n nolintguard)" && \
		go vet -vettool="$$nolintguard" -require-justification ./...

lint-golangci:
	@echo "Running golangci-lint..."
	$(GOLANGCI_LINT) run ./...

# Applies the automatic fixes golangci-lint offers, then lints.
lint-fix:
	$(GOLANGCI_LINT) run --fix ./...
	$(MAKE) lint
