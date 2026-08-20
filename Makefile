# Package patterns the convention checks run against. The tools in this
# module are their own first consumer, so a rule that does not hold here is
# a rule this module should not be asking of anyone else.
SELF ?= ./cmd/... ./internal/...

# GOOS values the lint gate covers. golangci-lint analyses one GOOS per run,
# and this module's path handling differs by platform, so a single run would
# leave one separator convention unread.
LINT_GOOS ?= linux darwin windows

# The reachability analyser, pinned. It is a separate binary from the
# toolchain that builds this module, so a floating version would let the two
# drift until the analysis stops matching the language the code is written
# in.
DEADCODE_VERSION ?= v0.48.0

# ///// Canonical form /////
.PHONY: tidy fmt

# -diff reports what tidying would change and writes nothing, so a target
# that check depends on cannot alter the tree it is verifying.
tidy: # ensure go.mod/go.sum are canonical
	@go mod tidy -diff || { echo ""; echo "FAIL: go.mod/go.sum not tidy. Run 'go mod tidy' and commit."; exit 1; }

fmt: # apply gofumpt and goimports
	@ok=1; \
	 command -v gofumpt   >/dev/null 2>&1 || { echo "gofumpt not found: go install mvdan.cc/gofumpt@latest"; ok=0; }; \
	 command -v goimports >/dev/null 2>&1 || { echo "goimports not found: go install golang.org/x/tools/cmd/goimports@latest"; ok=0; }; \
	 [ $$ok -eq 1 ] || exit 1
	gofumpt -w .
	goimports -w .

# ///// Static analysis /////
.PHONY: lint vet testpair deadcode

# The config is verified before it is used. `run` accepts settings the schema
# rejects, and CI verifies before it lints. Without this line the two
# disagree, and the disagreement surfaces only on a push.
lint: # run golangci-lint once per supported GOOS
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found: https://golangci-lint.run/usage/install/"; exit 1; }
	@golangci-lint config verify
	@for goos in $(LINT_GOOS); do \
		echo "golangci-lint: GOOS=$$goos"; \
		GOOS=$$goos golangci-lint run || exit 1; \
	done

vet: # run go vet
	go vet ./...

# Run from source rather than through `go tool`, because the build under
# test is the one in the working tree and not whichever version the module
# graph resolves to.
testpair: # verify 1:1 source/test file pairing
	go run ./cmd/testpair $(ARGS) $(SELF)

deadcode: # report unreachable exported symbols
	@command -v deadcode >/dev/null 2>&1 || { echo "deadcode not found: go install golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION)"; exit 1; }
	go run ./cmd/deadcode $(ARGS) $(SELF)

# ///// Security /////
.PHONY: vulncheck

vulncheck: # scan dependencies for known CVEs
	@command -v govulncheck >/dev/null 2>&1 || { echo "govulncheck not found: go install golang.org/x/vuln/cmd/govulncheck@latest"; exit 1; }
	govulncheck ./...

# ///// Behavior /////
.PHONY: test coverage

# The race detector needs cgo and a C compiler, which a Go-only workstation
# has no other reason to carry. CI runs the same suite under -race on
# runners that ship one.
test: # run the full test suite
	go test -count=1 ./...

coverage: # run tests and enforce per-package / total thresholds
	@command -v go-test-coverage >/dev/null 2>&1 || { echo "go-test-coverage not found: go install github.com/vladopajic/go-test-coverage/v2@latest"; exit 1; }
	go test -coverprofile=coverage.out -covermode=atomic $(SELF)
	go-test-coverage --config .testcoverage.yml

.PHONY: clean

clean: # remove the coverage profile
	rm -f coverage.out

# ///// Aggregates /////
.PHONY: check

check: tidy lint vet testpair deadcode vulncheck test coverage # local CI mirror
