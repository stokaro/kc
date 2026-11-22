# Developer shortcuts. The lint tools are pinned in the go.mod tool block and
# run through `go tool`, so no separate install step is needed.

GOLANGCI_LINT := go tool golangci-lint

.PHONY: all build test vet lint lint-golangci lint-nolintguard lint-fix

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
	@echo "Running golangci-lint..."
	$(GOLANGCI_LINT) run ./...

# Applies the automatic fixes golangci-lint offers, then lints.
lint-fix:
	$(GOLANGCI_LINT) run --fix ./...
	$(MAKE) lint
