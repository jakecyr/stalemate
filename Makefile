.PHONY: build test vet fmt check check-install live-test run

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

check-install: build
	sh -n install.sh docker/entrypoint.sh
	python3 -m unittest discover -s scripts -p 'test_*.py' -v

live-test: build
	python3 scripts/live_e2e.py

run: build
	./bin/stalemate serve
