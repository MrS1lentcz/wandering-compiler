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
	@echo "TODO: apply out/migrations/*.up.sql then *.down.sql against ephemeral postgres:16-alpine"
