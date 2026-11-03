.PHONY: run test lint build docker

run:
	go run ./cmd/server

test:
	go test -race ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./...

build:
	CGO_ENABLED=0 go build -o bin/server ./cmd/server

docker:
	docker build -t stripe-webhooks-go .
