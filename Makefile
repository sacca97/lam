BINARY  := lam
MODULE  := github.com/sacca97/lam
PREFIX  ?= $(HOME)/.local
BINDIR  ?= $(PREFIX)/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/app.version=$(VERSION)

# libghostty-vt (terminal emulator core) is built once into $(LIBGHOSTTY) by
# scripts/build-libghostty.sh (pinned Zig + Ghostty commit); cgo finds it via pkg-config.
LIBGHOSTTY ?= $(HOME)/.cache/lam/libghostty
export PKG_CONFIG_PATH := $(LIBGHOSTTY)/share/pkgconfig:$(PKG_CONFIG_PATH)

.DEFAULT_GOAL := build
.PHONY: build install release uninstall test test-race vet fmt fmt-check check clean help deps

help: ## Show targets
	@grep -E '^[a-z][a-z0-9-]*:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

deps: ## Build libghostty-vt if needed (downloads Zig, first run only)
	@scripts/build-libghostty.sh

build: deps ## Build the lam executable
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/lam

install: build ## Install to $(BINDIR) and restart the daemon if installed
	install -d "$(BINDIR)"
	install -m 755 "$(BINARY)" "$(BINDIR)/$(BINARY)"
	@echo "installed $(BINDIR)/$(BINARY)"
	@if [ -f $(HOME)/.config/systemd/user/lam-daemon.service ]; then \
		$(BINDIR)/$(BINARY) install-service && systemctl --user restart lam-daemon.service; \
	fi
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "warning: $(BINDIR) is not in PATH";; esac

release: ## Build the release archives and prepare the tag: make release TAG=v0.2.0
	@scripts/release.sh $(TAG)

uninstall: ## Remove the installed binary
	rm -f "$(BINDIR)/$(BINARY)"

test: deps ## Run tests
	go test ./...

test-race: deps ## Run tests with the race detector
	go test -race ./...

vet: deps ## Run go vet
	go vet ./...

fmt: ## Format Go code
	find cmd internal -name '*.go' -print0 | xargs -0 gofmt -w

fmt-check: ## Check Go formatting
	@unformatted="$$(find cmd internal -name '*.go' -print0 | xargs -0 gofmt -l)"; \
	if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi

check: fmt-check vet test ## Check formatting, vet, and tests

clean: ## Remove build output
	rm -f $(BINARY)
