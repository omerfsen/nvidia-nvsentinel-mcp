# nvidia-nvsentinel-mcp. Run `make help` for targets.
SHELL := /bin/bash
export GOTOOLCHAIN := local
BINARY := bin/nvidia-nvsentinel-mcp
IMAGE ?= nvsentinel-mcp:local
LINT_IMAGE := golangci/golangci-lint:v2.15.0
RESULTS := $(CURDIR)/docs/test-results
VERSION := $(shell cat version.txt)

.PHONY: help build lint test docs image lab-up lab-install lab-fixture lab-token lab-down e2e e2e-image

help:
	@echo "  make build        build $(BINARY)"
	@echo "  make lint         gofmt, go vet and golangci-lint with upstream NVSentinel's config"
	@echo "  make test         unit and MCP tests with client-go's fake client; no cluster"
	@echo "  make docs         regenerate docs/tools.md and docs/tools.json from the server"
	@echo "  make image        build the image locally"
	@echo "  make lab-up       create the kind cluster"
	@echo "  make lab-install  install NVSentinel through omerfsen.aicr (AICR_DIR=<checkout>)"
	@echo "  make lab-fixture  simulated DCGM, labels, workload, the server's RBAC"
	@echo "  make lab-token    mint the server's short-lived kubeconfigs"
	@echo "  make e2e          run the scenarios through the server on the lab; writes docs/test-results"
	@echo "  make e2e-image    the same, with the server in its container (IMAGE=<ref>)"
	@echo "  make lab-down     delete the kind cluster"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) .

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go vet -tags e2e ./tests/...
	docker run --rm -v "$(CURDIR):/w" -w /w -e GOFLAGS=-buildvcs=false $(LINT_IMAGE) golangci-lint run ./...

test:
	go test -race -count=1 ./pkg/...

docs:
	go test -count=1 ./pkg/server -run TestToolDocsMatchTheServer -update

image:
	docker build -t $(IMAGE) .

lab-up:
	lab/up.sh

lab-install:
	lab/install-nvsentinel.sh

lab-fixture:
	lab/fixture.sh

lab-token:
	lab/mint-kubeconfig.sh

lab-down:
	lab/down.sh

e2e: build
	mkdir -p $(RESULTS)
	E2E_BINARY=$(CURDIR)/$(BINARY) E2E_RESULTS=$(RESULTS) go test -tags e2e -count=1 -v -timeout 40m ./tests/e2e/

e2e-image:
	E2E_IMAGE=$(IMAGE) go test -tags e2e -count=1 -v -timeout 40m ./tests/e2e/
