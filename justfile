binary := "bin/mirage"
image := "ghcr.io/SwissDataScienceCenter/mirage"
tag := "dev"
ginkgo := "go tool ginkgo"

# Show the available recipes.
default:
    @just --list

# Lint, test and build.
all: lint test build

# Build the static binary.
build:
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o {{ binary }} ./cmd/mirage

# Run the Ginkgo suites.
test:
    {{ ginkgo }} -r --randomize-all --randomize-suites --keep-going --race --cover --coverprofile=coverage.out

# Run the suites under plain `go test`, e.g. `just test-go ./internal/decide`.
test-go pkg="./...":
    go test -race {{ pkg }}

# Open the coverage profile written by `just test`.
coverage: test
    go tool cover -html=coverage.out

# Run both linters. CI runs them as two separate steps, see .github/workflows/ci.yml.
lint: vet golangci-lint

vet:
    go vet ./...

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
