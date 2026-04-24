.PHONY: configure install build schemagen test test-apply test-apply-pg \
        e2e audit cover up seed nuke neoc migrate makemigrations loadtest \
        help

# wandering-compiler is a CLI tool (compiler), not a service stack. Some
# of the conventions-global/tooling.md "Core Targets" map cleanly
# (configure / install / build / test / e2e / audit / nuke), others
# describe operations on a deployed application (up / seed / migrate /
# loadtest) that don't apply to a compiler. Non-applicable targets keep
# their conventional name and emit a one-line explanation pointing at
# the right surface — uniformity over silent absence.

# Apply-roundtrip matrix. Each listed major version spins up its own
# ephemeral container and runs every applicable fixture. Override on
# the command line to narrow the matrix during development, e.g.
#
#   make test-apply-pg PG_VERSIONS=18
#
# MYSQL_VERSIONS is reserved for Layer C (MySQL emitter stub +
# catalog); empty until the emitter lands.
PG_VERSIONS    ?= 14 15 16 17 18
MYSQL_VERSIONS ?=

help:
	@echo "wandering-compiler — Makefile targets"
	@echo ""
	@echo "  Build / install:"
	@echo "    configure    — verify go + protoc are installed"
	@echo "    install      — configure + build"
	@echo "    build        — compile the wc CLI to srcgo/domains/compiler/bin/wc"
	@echo "    schemagen    — regenerate proto Go bindings under srcgo/pb/"
	@echo ""
	@echo "  Testing:"
	@echo "    test         — unit tests (go test ./...)"
	@echo "    e2e          — e2e classifier matrix (build-tag e2e); needs Docker for PG containers"
	@echo "    test-apply   — apply-roundtrip across the dialect matrix (PG_VERSIONS, MYSQL_VERSIONS)"
	@echo "    audit        — go vet + cross-package coverage report"
	@echo "    cover        — write merged coverage profile to /tmp/cover.out"
	@echo ""
	@echo "  Cleanup:"
	@echo "    nuke         — remove build artifacts + stop ephemeral test containers"
	@echo ""
	@echo "  N/A for this project type (compiler tool, not a service stack):"
	@echo "    up / seed / neoc / migrate / makemigrations / loadtest"

configure:
	@command -v go      >/dev/null 2>&1 || { echo "go not found in PATH"; exit 1; }
	@command -v protoc  >/dev/null 2>&1 || { echo "protoc not found in PATH (brew install protobuf or apt-get install protobuf-compiler)"; exit 1; }
	@echo "configure ok: go $$(go version | awk '{print $$3}') / protoc $$(protoc --version | awk '{print $$2}')"

install: configure build
	@echo "install ok: srcgo/domains/compiler/bin/wc"

build:
	@mkdir -p srcgo/domains/compiler/bin
	cd srcgo && go build -o domains/compiler/bin/wc ./domains/compiler/cmd/cli

schemagen:
	@mkdir -p srcgo/pb
	protoc \
		--proto_path=proto \
		--go_out=srcgo/pb \
		--go_opt=module=github.com/MrS1lentcz/wandering-compiler/srcgo/pb \
		proto/w17/db.proto \
		proto/w17/field.proto \
		proto/w17/pg/field.proto \
		proto/w17/pg/module.proto \
		proto/w17/pg/project.proto \
		proto/domains/compiler/types/ir.proto \
		proto/domains/compiler/types/plan.proto

test:
	cd srcgo && go test ./...

# E2E classifier matrix runner. Spins ephemeral PG containers per major
# version (default 18; override with PG_VERSIONS).
e2e:
	cd srcgo && PG_VERSIONS="$(PG_VERSIONS)" go test -tags=e2e -count=1 -timeout=600s ./domains/compiler/e2e/

# Aggregate apply-roundtrip — every (dialect, version) pair in the matrix.
# Sequential by default; per-version targets can be driven in parallel via
# `make -j` when I/O isn't the bottleneck.
test-apply: test-apply-pg

test-apply-pg:
	@for v in $(PG_VERSIONS); do \
		echo ""; \
		echo "=== postgres $$v ==="; \
		scripts/test-apply.sh postgres $$v; \
	done

# Audit = go vet + cross-package coverage. Floor recorded in
# iteration-1-coverage.md §3.3-ter (94.3% as of 2026-04-25).
audit:
	cd srcgo && go vet ./...
	cd srcgo && ./cover-all.sh > /tmp/cover.out
	@cd srcgo && go tool cover -func=/tmp/cover.out | tail -1

cover:
	cd srcgo && ./cover-all.sh > /tmp/cover.out
	@echo "coverage profile written to /tmp/cover.out"
	@cd srcgo && go tool cover -func=/tmp/cover.out | tail -1

# Nuke removes build artifacts + any leftover ephemeral test containers.
# Doesn't touch the apply test fixtures (those are committed); only the
# generated `bin/` and `pb/` trees, plus dangling Docker containers
# whose names match the test-apply pattern.
nuke:
	rm -rf srcgo/domains/compiler/bin
	rm -rf srcgo/pb
	@docker ps -a --format '{{.Names}}' 2>/dev/null | grep -E '^wc-test-apply-' | xargs -r docker rm -f >/dev/null 2>&1 || true
	@echo "nuke ok: build artifacts + pb/ + ephemeral containers removed"

# --- Targets that don't apply to a compiler tool (kept for convention
# parity per tooling.md §Core Targets) ------------------------------------

up:
	@echo "up: N/A for wandering-compiler (no service stack). Build the CLI with 'make build' and invoke directly."

seed:
	@echo "seed: N/A for wandering-compiler (no DB to seed). Test fixtures live under srcgo/domains/compiler/testdata/."

neoc: nuke
	@echo "neoc: ran nuke; seed phase is N/A for a compiler tool."

migrate:
	@echo "migrate: wandering-compiler GENERATES migrations; it doesn't apply them to itself."
	@echo "         For the apply-roundtrip across the dialect matrix run 'make test-apply'."
	@echo "         Downstream projects apply generated migrations via their own pipeline."

makemigrations:
	@echo "makemigrations: wandering-compiler IS the migration generator."
	@echo "                Build with 'make build' and run 'bin/wc generate <input.proto>'."

loadtest:
	@echo "loadtest: N/A for wandering-compiler (no runtime to load-test)."
