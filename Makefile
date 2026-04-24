.PHONY: build schemagen test test-apply test-apply-pg

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

# Aggregate target — runs every (dialect, version) pair in the matrix.
# Sequential by default; individual per-version targets can be driven
# in parallel via `make -j` when I/O isn't the bottleneck.
test-apply: test-apply-pg

test-apply-pg:
	@for v in $(PG_VERSIONS); do \
		echo ""; \
		echo "=== postgres $$v ==="; \
		scripts/test-apply.sh postgres $$v; \
	done
