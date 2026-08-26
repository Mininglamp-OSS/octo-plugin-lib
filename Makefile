.DEFAULT_GOAL := help

GO ?= go

.PHONY: help fmt test test-mysql vet verify

help:
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\n"} /^[a-zA-Z_-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go source
	$(GO) fmt ./...

test: ## Run unit tests
	$(GO) test -count=1 ./...

test-mysql: ## Run all tests and require the MySQL DSNs
	OCTO_PLUGIN_LIB_REQUIRE_MYSQL=1 $(GO) test -count=1 ./...

vet: ## Run Go static analysis
	$(GO) vet ./...

verify: fmt test vet ## Run all deterministic delivery gates
