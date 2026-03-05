set windows-shell := ["pwsh.exe", "-NoLogo", "-Command"]

# --- GLOBAL VARIABLES ---
bin_dir         := "bin"
bin_name        := if os() == "windows" { "go-clean-unused.exe" } else { "go-clean-unused" }
bin_path        := bin_dir + "/" + bin_name
slash           := if os() == "windows" { "\\" } else { "/" }

default: 
    @just --choose

lint:
    golangci-lint run --fix

fmt:
    @echo "=> Modernizing code (go fix)..."
    go fix ./...
    @echo "=> Formatting code (gofumpt)..."
    gofumpt -l -w .
    @echo "=> Fixing imports (goimports)..."
    goimports -w .

verify: fmt lint test build

vendor:
    go mod tidy
    go mod vendor
    go mod tidy

build: clean
    go build -o {{bin_path}} .

exec:
    .{{slash}}{{bin_path}}

build_run: build exec

clean:
    @go clean
    {{ if os() == "windows" { "if (Test-Path " + bin_dir + ") { Remove-Item -Recurse -Force " + bin_dir + " }" } else { "rm -rf " + bin_dir } }}

test path="./...":
    gotestsum --format-hide-empty-pkg --format-icons octicons {{path}}

test-watch path="./...":
    gotestsum --format-hide-empty-pkg --format-icons octicons --watch {{path}}

test-coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out

security:
    go vet ./...
    govulncheck ./...
    gosec ./...
