.PHONY: build schemagen test test-apply

build:
	@echo "TODO: go build -o srcgo/domains/compiler/bin/wc ./srcgo/domains/compiler/cmd/cli"

schemagen:
	@echo "TODO: compile proto/w17/*.proto -> srcgo/pb/w17/"

test:
	@echo "TODO: go test ./..."

test-apply:
	@echo "TODO: apply out/migrations/*.up.sql then *.down.sql against ephemeral postgres:16-alpine"
