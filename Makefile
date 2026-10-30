GO                ?= go
LINTER            ?= golangci-lint
ALIGNER           ?= betteralign
RUMDL             ?= rumdl
BENCHSTAT         ?= benchstat
BENCH_COUNT       ?= 6
BENCH_REF         ?= bench_baseline.txt
FUZZ_TIME         ?= 30s

.PHONY: check ci

check: verify tidy fmt markdown-lint vet lint-fix align-fix test test-race fuzz
ci: download tools-ci verify tidy-check fmt-check vet lint align test

.PHONY: test test-race test-integration test-integration-race

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-integration:
	$(GO) test -tags cm_auth_live -run '^TestLiveAuthenticatedDayZMasterServerQuery$$' -v -count=1 ./gameservers

test-integration-race:
	$(GO) test -tags cm_auth_live -race -run '^TestLiveAuthenticatedDayZMasterServerQuery$$' -v -count=1 ./gameservers

.PHONY: bench bench-fast bench-reset

bench:
	@tmp=$$(mktemp); \
	$(GO) test ./... -run=^$$ -bench 'Benchmark' -benchmem -count=$(BENCH_COUNT) | tee "$$tmp"; \
	if [ -f "$(BENCH_REF)" ]; then \
		$(BENCHSTAT) "$(BENCH_REF)" "$$tmp"; \
	else \
		cp "$$tmp" "$(BENCH_REF)" && echo "Baseline saved to $(BENCH_REF)"; \
	fi; \
	rm -f "$$tmp"

bench-fast:
	$(GO) test ./... -run=^$$ -bench 'Benchmark' -benchmem

bench-reset:
	rm -f "$(BENCH_REF)"

.PHONY: fuzz

fuzz:
	$(GO) test . -run='^$$' -fuzz='^FuzzDecodeFrame$$' -fuzztime=$(FUZZ_TIME)
	$(GO) test . -run='^$$' -fuzz='^FuzzDecodeMulti$$' -fuzztime=$(FUZZ_TIME)

.PHONY: download verify vet tidy tidy-check fmt fmt-check lint lint-fix align align-fix

download:
	$(GO) mod download

verify:
	$(GO) mod verify

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

tidy-check:
	@$(GO) mod tidy
	@git diff --stat --exit-code -- go.mod go.sum || ( \
		echo "go mod tidy: repository is not tidy"; \
		exit 1; \
	)

fmt:
	$(GO) fmt ./...

fmt-check:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "$$files"; \
		echo "gofmt: files need formatting"; \
		exit 1; \
	fi

lint:
	$(LINTER) run ./...

lint-fix:
	$(LINTER) run --fix ./...

align:
	$(ALIGNER) ./...

align-fix:
	-$(ALIGNER) -apply ./...
	$(ALIGNER) ./...

.PHONY: tools tools-ci tool-golangci-lint tool-betteralign tool-benchstat

tools: tool-golangci-lint tool-betteralign tool-benchstat
tools-ci: tool-golangci-lint tool-betteralign

tool-golangci-lint:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

tool-betteralign:
	$(GO) install github.com/dkorunic/betteralign/cmd/betteralign@latest

tool-benchstat:
	$(GO) install golang.org/x/perf/cmd/benchstat@latest

.PHONY: release-notes

release-notes:
	@awk '\
	/^<!--/,/^-->/ { next } \
	/^## \[[0-9]+\.[0-9]+\.[0-9]+\]/ { if (found) exit; found=1; next } \
	found { \
		if (/^## \[/) { exit } \
		if (/^$$/) { flush(); print; next } \
		if (/^\* / || /^- /) { flush(); buf=$$0; next } \
		if (/^###/ || /^\[/) { flush(); print; next } \
		sub(/^[ \t]+/, ""); sub(/[ \t]+$$/, ""); \
		if (buf != "") { buf = buf " " $$0 } else { buf = $$0 } \
		next \
	} \
	function flush() { if (buf != "") { print buf; buf = "" } } \
	END { flush() } \
	' CHANGELOG.md

.PHONY: markdown-lint markdown-fix

define run-rumdl
	@if command -v $(RUMDL) &>/dev/null; then \
		$(RUMDL) $(1); \
	else \
		echo "WARN: $(RUMDL) not found; skipping markdown lint."; \
		echo 'WARN: Install it https://github.com/rvben/rumdl'; \
	fi
endef

markdown-lint:
	$(call run-rumdl,check)

markdown-fix:
	$(call run-rumdl,check --fix)
