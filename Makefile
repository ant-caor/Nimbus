.PHONY: all test race bench lint fmt tidy integration

# Default: format, run unit tests with the race detector, lint, and benchmarks.
all: fmt race lint bench

# Unit tests (library module).
test:
	go test ./...

# Unit tests with the race detector.
race:
	go test -race ./...

# Benchmarks (no tests, just benchmarks, with allocation stats).
bench:
	go test -run='^$$' -bench=. -benchmem ./...

# Lint with golangci-lint (https://golangci-lint.run).
lint:
	golangci-lint run

# Format all Go files in place.
fmt:
	gofmt -w .

# Keep both module files tidy.
tidy:
	go mod tidy
	cd test/integration && go mod tidy

# Integration tests: spins up Redis and the Pub/Sub emulator via testcontainers.
# Requires a running Docker daemon.
integration:
	cd test/integration && go test -count=1 ./...
