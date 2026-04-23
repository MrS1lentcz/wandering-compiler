.PHONY: build schemagen test test-apply

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
		proto/domains/compiler/types/ir.proto \
		proto/domains/compiler/types/plan.proto

test:
	cd srcgo && go test ./...

test-apply:
	@set -eu; \
	command -v docker >/dev/null 2>&1 || { echo "test-apply: docker not found"; exit 1; }; \
	echo "test-apply: starting ephemeral postgres:18-alpine"; \
	CID=$$(docker run --rm -d -e POSTGRES_PASSWORD=test postgres:18-alpine); \
	trap "docker kill $$CID >/dev/null 2>&1 || true" EXIT; \
	for i in $$(seq 1 60); do \
		docker exec $$CID pg_isready -U postgres -q 2>/dev/null && break; \
		sleep 1; \
	done; \
	docker exec $$CID pg_isready -U postgres -q >/dev/null || { echo "test-apply: postgres never became ready"; exit 1; }; \
	for dir in srcgo/domains/compiler/testdata/*/; do \
		name=$$(basename $$dir); \
		case $$name in alter|alter_refuse) continue;; esac; \
		db=test_$$name; \
		echo "--- iter1: $$name ---"; \
		docker exec $$CID psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $$db;" >/dev/null; \
		docker exec $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 -c "CREATE EXTENSION IF NOT EXISTS hstore;" >/dev/null; \
		docker exec $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 -c "CREATE EXTENSION IF NOT EXISTS citext;" >/dev/null; \
		docker exec $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 -c "CREATE EXTENSION IF NOT EXISTS pg_trgm;" >/dev/null; \
		docker exec $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 -c "CREATE SCHEMA IF NOT EXISTS reporting;" >/dev/null; \
		for phase in up down up; do \
			echo "  $$phase"; \
			docker exec -i $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 < $${dir}expected.$${phase}.sql >/dev/null; \
		done; \
	done; \
	for dir in srcgo/domains/compiler/testdata/alter/*/; do \
		name=$$(basename $$dir); \
		db=test_alter_$$name; \
		echo "--- alter: $$name ---"; \
		docker exec $$CID psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $$db;" >/dev/null; \
		docker exec $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 -c "CREATE EXTENSION IF NOT EXISTS hstore;" >/dev/null; \
		docker exec $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 -c "CREATE EXTENSION IF NOT EXISTS citext;" >/dev/null; \
		docker exec $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 -c "CREATE EXTENSION IF NOT EXISTS pg_trgm;" >/dev/null; \
		docker exec $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 -c "CREATE SCHEMA IF NOT EXISTS reporting;" >/dev/null; \
		echo "  prev.up"; \
		rm -rf /tmp/wc-apply-$$name; \
		(cd srcgo && go run ./domains/compiler/cmd/cli generate --iteration-1 --no-applied-state -I "$$(pwd)/../proto" -o "/tmp/wc-apply-$$name" "$$(pwd)/../$${dir}prev.proto") >/dev/null; \
		PREV_UP=$$(ls /tmp/wc-apply-$$name/migrations/*.up.sql); \
		PREV_DOWN=$$(ls /tmp/wc-apply-$$name/migrations/*.down.sql); \
		docker exec -i $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 < "$$PREV_UP" >/dev/null; \
		echo "  diff.up"; \
		docker exec -i $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 < $${dir}expected.up.sql >/dev/null; \
		echo "  diff.down"; \
		docker exec -i $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 < $${dir}expected.down.sql >/dev/null; \
		echo "  prev.down"; \
		docker exec -i $$CID psql -U postgres -d $$db -v ON_ERROR_STOP=1 < "$$PREV_DOWN" >/dev/null; \
		rm -rf /tmp/wc-apply-$$name; \
	done; \
	echo "test-apply: all iter-1 + alter fixtures applied and rolled back cleanly"
