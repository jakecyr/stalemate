.PHONY: build test vet fmt check live-test run

build:
	go build -trimpath -o bin/stalemate ./cmd/stalemate

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test -race ./...

live-test: build
	python3 scripts/live_e2e.py

run: build
	./bin/stalemate serve
