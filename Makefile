.PHONY: build test vet fmt run all

all: vet test build

build:
	go build ./...

test:
	go test ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -w .

run:
	go run ./cmd/relay
