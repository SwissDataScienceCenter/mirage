binary := "bin/mirage"
image := "ghcr.io/SwissDataScienceCenter/mirage"
tag := "dev"
ginkgo := "go tool ginkgo"

# The Kubernetes version the integration suite runs against. Pinned deliberately:
# the README claims 1.29+, so the version the suite proves is a choice to record
# rather than a default to inherit. See ADR 0007.
k8s_version := "1.34.1"

# Show the available recipes.
default:
    @just --list

# Lint, test and build.
all: lint test build

# Build the static binary.
build:
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o {{ binary }} ./cmd/mirage

# Run the Ginkgo suites.
#
# --skip-package because `-r` finds suites by scanning for _test.go filenames,
# which happens before build constraints are applied: without it ginkgo tries to
# compile test/integration and fails with "build constraints exclude all Go
# files". The tag stops the suite running; this is what stops it being looked at.
test:
    {{ ginkgo }} -r --skip-package=test/integration --randomize-all --randomize-suites --keep-going --race --cover --coverprofile=coverage.out

# Run the suites under plain `go test`, e.g. `just test-go ./internal/decide`.
test-go pkg="./...":
    go test -race {{ pkg }}

# Download the envtest control-plane binaries and print where they landed.
envtest:
    @go tool setup-envtest use {{ k8s_version }} -p path

# Run the integration suite against a real etcd and kube-apiserver.
#
# Behind the `integration` build tag, so `just test` neither compiles nor runs it
# and stays fast. One control plane per suite, so no --procs: the specs share it.
test-integration:
    KUBEBUILDER_ASSETS="$(just envtest)" \
        {{ ginkgo }} --tags=integration --randomize-all --race ./test/integration

# Open the coverage profile written by `just test`.
coverage: test
    go tool cover -html=coverage.out

# Run both linters. CI runs them as two separate steps, see .github/workflows/ci.yml.
lint: vet golangci-lint

vet:
    go vet ./...
    # ./... does not reach the integration suite, whose files are all behind the
    # tag. Vetting needs no envtest binaries, so it runs here rather than being
    # left to `just test-integration` — otherwise the suite is the one package
    # nothing lints until someone runs it.
    go vet -tags=integration ./test/integration

golangci-lint:
    golangci-lint run

fmt:
    go fmt ./...

tidy:
    go mod tidy

# Build the container image.
image:
    docker build -t {{ image }}:{{ tag }} .

clean:
    rm -rf bin coverage.out
