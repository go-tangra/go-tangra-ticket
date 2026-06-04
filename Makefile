VERSION ?= 1.0.0
TICKET_IMAGE_NAME ?= ghcr.io/go-tangra/go-tangra-ticket
TICKET_IMAGE_TAG ?= $(VERSION)

.PHONY: build-server
build-server:
	@echo "Building Ticket server..."
	@go build -ldflags "-X main.version=$(VERSION)" -o ./bin/ticket-server ./cmd/server

.PHONY: run-server
run-server:
	@go run ./cmd/server -c ./configs

.PHONY: api
api:
	@buf dep update
	@buf generate
	@buf build -o cmd/server/assets/descriptor.bin

.PHONY: ts-client
ts-client:
	@# Generate the frontend TS client with the PINNED generator (NOT @latest).
	@GOBIN=$$(pwd)/bin go install github.com/go-kratos/protoc-gen-typescript-http@v0.0.0-20260525125049-694cf6cd0529
	@PATH="$$(pwd)/bin:$$PATH" buf generate --template buf.typescript.gen.yaml

.PHONY: ent
ent:
	@# NOTE: the ent CLI (v0.14.5) is incompatible with newer tablewriter.
	@# Pin it for the duration of generation, then drop the pin.
	@go mod edit -replace github.com/olekukonko/tablewriter=github.com/olekukonko/tablewriter@v0.0.5
	@GOFLAGS=-mod=mod go run entgo.io/ent/cmd/ent generate \
		--feature sql/modifier --feature sql/upsert --feature sql/lock \
		./internal/data/ent/schema; \
		status=$$?; \
		go mod edit -dropreplace github.com/olekukonko/tablewriter; \
		go mod tidy; \
		exit $$status

.PHONY: wire
wire:
	@cd ./cmd/server && wire

.PHONY: openapi
openapi:
	@buf generate --template buf.openapi.gen.yaml

.PHONY: generate
generate: api ent wire
	@echo "Generation complete!"

.PHONY: docker
docker:
	@docker build -t $(TICKET_IMAGE_NAME):$(TICKET_IMAGE_TAG) -t $(TICKET_IMAGE_NAME):latest \
		--build-arg APP_VERSION=$(VERSION) -f ./Dockerfile .

.PHONY: test
test:
	@go test ./...

.PHONY: clean
clean:
	@rm -rf ./bin
