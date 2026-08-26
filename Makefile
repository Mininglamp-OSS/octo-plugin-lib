.DEFAULT_GOAL := help

GO ?= go

.PHONY: help fmt test vet verify

help:
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\n"} /^[a-zA-Z_-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go source
	$(GO) fmt ./...

test: ## Run unit tests
	$(GO) test ./...

vet: ## Run Go static analysis
	$(GO) vet ./...

verify: fmt test vet ## Run all deterministic delivery gates
