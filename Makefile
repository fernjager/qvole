BIN      := qvole
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null)
GO       ?= go
MAIN_DIR := ./cmd/qvole
BIN_DIR  := bin
LDFLAGS  := -X main.version=$(VERSION)

PLATFORMS := linux/amd64 linux/arm64 linux/arm linux/mips linux/mipsle \
             darwin/amd64 darwin/arm64 freebsd/amd64 windows/amd64 windows/arm64

.PHONY: all build install test test-unit test-integration lint release clean help

all: build

##@ Build

build: ## Build the binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BIN) $(MAIN_DIR)

PREFIX ?= /usr/local

install: build ## Install the binary to PREFIX/bin
	install -d $(PREFIX)/bin
	install -m 755 $(BIN_DIR)/$(BIN) $(PREFIX)/bin/$(BIN)

##@ Test

test: test-unit test-integration ## Run all tests

test-unit: ## Run unit tests
	$(GO) test -v -count=1 $(shell $(GO) list ./... | grep -v /tests)

test-integration: ## Run integration tests
	$(GO) test -v -count=1 ./tests/

##@ Check

lint: ## Run golangci-lint
	golangci-lint run ./...

##@ Release

release: clean ## Build binaries for all release platforms
	@mkdir -p $(BIN_DIR)
	@set -e; \
	for p in $(PLATFORMS); do \
		OS=$${p%/*}; \
		ARCH=$${p#*/}; \
		EXT=$$([ "$$OS" = "windows" ] && echo ".exe" || echo ""); \
		TARGET="$(BIN_DIR)/$(BIN)-$${OS}-$${ARCH}$${EXT}"; \
		echo "Building $$TARGET"; \
		case "$$ARCH" in \
			mips|mipsle) GOMIPS=softfloat GOOS=$$OS GOARCH=$$ARCH $(GO) build -ldflags="-s -w $(LDFLAGS)" -o "$$TARGET" $(MAIN_DIR) ;; \
			arm)         GOARM=6 GOOS=$$OS GOARCH=$$ARCH $(GO) build -ldflags="$(LDFLAGS)" -o "$$TARGET" $(MAIN_DIR) ;; \
			*)           GOOS=$$OS GOARCH=$$ARCH $(GO) build -ldflags="$(LDFLAGS)" -o "$$TARGET" $(MAIN_DIR) ;; \
		esac; \
	done

##@ Clean

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	go clean

##@ Help

help: ## Show this help
	@echo 'Usage: make [target]'
	@echo
	@echo 'Targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'
	@echo
	@echo 'Platforms: $(PLATFORMS)'
