# Developer shortcuts. The lint tools are pinned in the go.mod tool block and
# run through `go tool`, so no separate install step is needed.

GOLANGCI_LINT := go tool golangci-lint

# golangci-lint reports only what changed since the merge base with this ref
# (see `issues.new-from-merge-base` in .golangci.yml). Override it to compare
# against another ref, for example `make lint LINT_BASE=main`.
LINT_BASE ?= origin/main

.PHONY: all build test vet lint lint-golangci lint-nolintguard lint-fix lint-all

all: build test lint

build:
	go build ./...

vet:
	go vet ./...

# The envtest suites need KUBEBUILDER_ASSETS; see CONTRIBUTING.md.
test:
	go test ./...

lint: lint-nolintguard lint-golangci

# Every //nolint, #nosec, //gosec: and //revive: directive must name what it
# silences and say why. nolintguard is not a golangci-lint plugin, so it runs
# through go vet's analyzer driver. It reads the whole tree, not only new code.
lint-nolintguard:
	@echo "Running nolintguard..."
	@nolintguard="$$(go tool -n nolintguard)" && \
		go vet -vettool="$$nolintguard" -require-justification ./...

lint-golangci:
	@echo "Running golangci-lint (new code since $(LINT_BASE))..."
	$(GOLANGCI_LINT) run --new-from-merge-base=$(LINT_BASE) ./...

# Applies the automatic fixes golangci-lint offers for new code, then lints.
lint-fix:
	$(GOLANGCI_LINT) run --fix --new-from-merge-base=$(LINT_BASE) ./...
	$(MAKE) lint

# Reports every finding in the tree, including the baseline that `make lint`
# does not show. It is expected to fail until the baseline is fixed.
lint-all:
	$(GOLANGCI_LINT) run --new-from-merge-base= ./...
