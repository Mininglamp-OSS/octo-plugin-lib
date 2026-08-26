.DEFAULT_GOAL := help

GO ?= go
PYTHON ?= python3

.PHONY: help fmt check-fmt test test-cross-language test-mysql vet verify

help:
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\n"} /^[a-zA-Z_-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go source
	$(GO) fmt ./...

check-fmt: ## Fail when Go source is not formatted
	@unformatted="$$(gofmt -l .)"; test -z "$$unformatted" || { printf 'Unformatted Go files:\n%s\n' "$$unformatted"; exit 1; }

test: ## Run unit tests
	$(GO) test -count=1 ./...

test-cross-language: ## Verify Canonical JSON and hash golden fixtures with Python stdlib
	$(PYTHON) scripts/verify_golden.py

test-mysql: ## Run all tests and require the MySQL DSNs
	OCTO_PLUGIN_LIB_REQUIRE_MYSQL=1 $(GO) test -count=1 ./...

vet: ## Run Go static analysis
	$(GO) vet ./...

verify: check-fmt test test-cross-language vet ## Run all deterministic delivery gates
