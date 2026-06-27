.PHONY: all test race bench lint fmt tidy integration

# Modules in this repo. The library modules ship to users; everything else is
# package main (examples/demo, never published) or test infra (test/integration).
LIB_MODULES        := . metrics invalidation/gcppubsub
# Every module with a go.mod, discovered rather than hand-listed so a newly
# added module is never silently skipped by tidy/lint (a stale examples/redisbus
# slipped through exactly that way once). Mirrors the dependabot-auto-tidy
# workflow, which enumerates modules the same way.
ALL_MODULES        := $(shell find . -name go.mod -not -path '*/vendor/*' -exec dirname {} \; | sed 's|^\./||' | sort)
# Modules with unit tests / benchmarks.
TESTABLE_MODULES   := $(LIB_MODULES)

# Each module is validated in isolation (GOWORK=off), the way a real consumer
# pulls it; the root go.work is a local-dev convenience only.
GO := GOWORK=off go

# Default: format, run unit tests with the race detector, lint, and benchmarks.
all: fmt race lint bench

# Unit tests across the testable modules.
test:
	@for d in $(TESTABLE_MODULES); do echo "== test $$d =="; (cd $$d && $(GO) test ./...) || exit 1; done

# Unit tests with the race detector.
race:
	@for d in $(TESTABLE_MODULES); do echo "== race $$d =="; (cd $$d && $(GO) test -race ./...) || exit 1; done

# Benchmarks (no tests, just benchmarks, with allocation stats). Hot paths live
# in the root module.
bench:
	$(GO) test -run='^$$' -bench=. -benchmem ./...

# Lint with golangci-lint (https://golangci-lint.run) across every module.
lint:
	@for d in $(ALL_MODULES); do echo "== lint $$d =="; (cd $$d && GOWORK=off golangci-lint run) || exit 1; done

# Format all Go files in place (module-agnostic; walks the whole repo).
fmt:
	gofmt -w .

# Keep every module's go.mod/go.sum tidy.
tidy:
	@for d in $(ALL_MODULES); do echo "== tidy $$d =="; (cd $$d && $(GO) mod tidy) || exit 1; done

# Integration tests: spins up Redis and the Pub/Sub emulator via testcontainers.
# Requires a running Docker daemon.
integration:
	cd test/integration && GOWORK=off go test -count=1 ./...
