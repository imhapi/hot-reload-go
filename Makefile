# Makefile for building hrhapi
.PHONY: build build-all clean test help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X 'main.version=$(VERSION)' -X 'main.buildTime=$(BUILD_TIME)'
OUTPUT_DIR := dist

TARGETS := \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64 \
	windows/arm64

help:
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

build:
	@echo "Building hrhapi for $(shell go env GOOS)/$(shell go env GOARCH)..."
	@go build -ldflags "$(LDFLAGS)" -o hrhapi .

build-all: clean|
	@echo "Building hrhapi for multiple platforms..."
	@mkdir -p $(OUTPUT_DIR)
	@for target in $(TARGETS); do \
		os=$$(echo $$target | cut -d'/' -f1); \
		arch=$$(echo $$target | cut -d'/' -f2); \
		echo "Building for $$os/$$arch..."; \
		if [ "$$os" = "windows" ]; then \
			output="$(OUTPUT_DIR)/hrhapi-$$os-$$arch.exe"; \
		else \
			output="$(OUTPUT_DIR)/hrhapi-$$os-$$arch"; \
		fi; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o $$output . && \
		echo "✓ Built: $$output"; \
	done
	@echo ""
	@echo "Build complete! Executables are in $(OUTPUT_DIR)/"
	@ls -lh $(OUTPUT_DIR) | grep hrhapi

clean:
	@echo "Cleaning build artifacts..."
	@rm -rf $(OUTPUT_DIR)
	@rm -f hrhapi hrhapi.exe
	@echo "Clean complete!"

test:
	@go test -v ./...

install: build
	@go install -ldflags "$(LDFLAGS)" .

