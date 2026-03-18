# =============================================================================
# fs-workflow Makefile
#
# Usage:
#   make build       Build the fs-workflow binary locally
#   make run         Run the workflow worker in the foreground
#   make fmt         Run gofmt across the codebase
#   make vet         Run go vet
#   make tidy        Run go mod tidy
#   make clean       Remove the locally-built binary
#   make test        Run unit tests
# =============================================================================

BINARY := fs-workflow

.PHONY: build build-release run fmt vet tidy clean test help

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

## build: Build the fs-workflow binary locally (requires Go 1.23+)
build:
	go build -o $(BINARY) .

## build-release: Build locally with full optimisations
build-release:
	go build -ldflags="-s -w" -o $(BINARY) .

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

## run: Start the workflow worker in the foreground
run: build
	./$(BINARY)

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

## test: Run all unit tests
test:
	go test ./...

# ---------------------------------------------------------------------------
# Go tooling
# ---------------------------------------------------------------------------

## fmt: Format all Go source files
fmt:
	gofmt -w .

## vet: Run go vet on all packages
vet:
	go vet ./...

## tidy: Run go mod tidy
tidy:
	go mod tidy

## clean: Remove the locally-built binary
clean:
	rm -f $(BINARY)

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

## help: Print this help message
help:
	@echo ""
	@echo "fs-workflow — Makefile targets"
	@echo ""
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | column -t -s ':'
	@echo ""
