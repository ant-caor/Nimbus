.PHONY: all test race bench lint fmt tidy integration

# Modules in this repo. The library modules ship to users; examples/demo are
# package main (never published) and test/integration is test infra.
LIB_MODULES        := . metrics invalidation/gcppubsub
ALL_MODULES        := $(LIB_MODULES) examples/cloudrun demo/local test/integration
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
