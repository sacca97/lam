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

# The GUI build (lam-gui) links Gio's Wayland and X11 backends. Without the
# xkbcommon-x11 development files it is built for Wayland only.
GUI_BINARY := lam-gui
GUI_TAGS   ?= gui$(shell pkg-config --exists xkbcommon-x11 2>/dev/null || echo ,nox11)
DESKTOP_DIR ?= $(HOME)/.local/share/applications
ICON_DIR    ?= $(HOME)/.local/share/icons
ICON_SIZES  := 16 32 48 64 128 256 512
APP_DIR     ?= $(HOME)/Applications
APP_BUNDLE  := $(APP_DIR)/lam.app

.DEFAULT_GOAL := build
.PHONY: build gui macapp cross-mac-gui install release uninstall test test-race vet fmt fmt-check check clean help deps

help: ## Show targets
	@grep -E '^[a-z][a-z0-9-]*:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

deps: ## Build libghostty-vt if needed (downloads Zig, first run only)
	@scripts/build-libghostty.sh

build: deps ## Build the lam executable
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/lam

gui: deps ## Build lam-gui, lam with the experimental Gio GUI (needs wayland/egl dev packages)
	CGO_ENABLED=1 go build -tags '$(GUI_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(GUI_BINARY) ./cmd/lam

install: build ## Install lam (and lam-gui when it builds) to $(BINDIR), restart the daemon if installed
	install -d "$(BINDIR)"
	install -m 755 "$(BINARY)" "$(BINDIR)/$(BINARY)"
	@echo "installed $(BINDIR)/$(BINARY)"
	@if $(MAKE) --no-print-directory gui >/dev/null 2>&1; then \
		install -m 755 "$(GUI_BINARY)" "$(BINDIR)/$(GUI_BINARY)"; \
		echo "installed $(BINDIR)/$(GUI_BINARY) ($(GUI_TAGS))"; \
		if [ "$$(uname -s)" = Linux ]; then \
			install -d "$(DESKTOP_DIR)"; \
			printf '[Desktop Entry]\nType=Application\nName=lam\nComment=Agent sessions\nExec=%s\nIcon=lam\nTerminal=false\nCategories=Development;\n' \
				"$(BINDIR)/$(GUI_BINARY)" > "$(DESKTOP_DIR)/lam.desktop"; \
			for s in $(ICON_SIZES); do \
				install -d "$(ICON_DIR)/hicolor/$${s}x$${s}/apps"; \
				install -m 644 assets/icon/png/lam-$$s.png "$(ICON_DIR)/hicolor/$${s}x$${s}/apps/lam.png"; \
			done; \
			gtk-update-icon-cache -q -t "$(ICON_DIR)/hicolor" 2>/dev/null || true; \
		fi; \
		if [ "$$(uname -s)" = Darwin ]; then $(MAKE) --no-print-directory macapp; fi; \
	else \
		echo "lam-gui not built (run 'make gui' to see why); lam is installed"; \
	fi
	@# Only refresh the service that runs this install: an install elsewhere
	@# (another PREFIX) must not repoint it.
	@if grep -qF "$(BINDIR)/$(BINARY) daemon" $(HOME)/.config/systemd/user/lam-daemon.service 2>/dev/null; then \
		$(BINDIR)/$(BINARY) install-service && systemctl --user restart lam-daemon.service; \
	fi
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "warning: $(BINDIR) is not in PATH";; esac

# macapp makes lam.app so Launchpad and Spotlight find the GUI. The bundle's
# launcher starts lam-gui from a login shell: an app opened from Finder does
# not get the PATH the agents (claude, codex...) are installed under.
macapp:
	rm -rf "$(APP_BUNDLE)"
	install -d "$(APP_BUNDLE)/Contents/MacOS"
	install -m 755 "$(GUI_BINARY)" "$(APP_BUNDLE)/Contents/MacOS/lam-gui"
	install -d "$(APP_BUNDLE)/Contents/Resources"
	install -m 644 assets/icon/lam.icns "$(APP_BUNDLE)/Contents/Resources/lam.icns"
	printf '<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict>\n<key>CFBundleName</key><string>lam</string>\n<key>CFBundleIdentifier</key><string>io.github.sacca97.lam</string>\n<key>CFBundleExecutable</key><string>lam-gui</string>\n<key>CFBundleIconFile</key><string>lam</string>\n<key>CFBundlePackageType</key><string>APPL</string>\n<key>CFBundleVersion</key><string>1</string>\n<key>CFBundleShortVersionString</key><string>$(patsubst v%,%,$(VERSION))</string>\n<key>LSMinimumSystemVersion</key><string>11.0</string>\n<key>NSHighResolutionCapable</key><true/>\n<key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>\n</dict></plist>\n' > "$(APP_BUNDLE)/Contents/Info.plist"
	@# Notifications need the bundle to be signed; an ad-hoc signature does.
	@if command -v codesign >/dev/null 2>&1; then codesign --force --deep -s - "$(APP_BUNDLE)" >/dev/null 2>&1 || echo "warning: could not sign $(APP_BUNDLE); notifications may not show"; fi
	@echo "installed $(APP_BUNDLE)"

cross-mac-gui: ## Build lam.app for macOS arm64 from Linux: make cross-mac-gui MACOS_SDK=/path/to/MacOSX.sdk
	@scripts/cross-darwin-gui.sh

release: ## Build the release archives and prepare the tag: make release TAG=v0.2.0
	@scripts/release.sh $(TAG)

uninstall: ## Remove the installed binaries
	rm -f "$(BINDIR)/$(BINARY)" "$(BINDIR)/$(GUI_BINARY)" "$(DESKTOP_DIR)/lam.desktop"
	@for s in $(ICON_SIZES); do rm -f "$(ICON_DIR)/hicolor/$${s}x$${s}/apps/lam.png"; done
	rm -rf "$(APP_BUNDLE)"

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
	rm -f $(BINARY) $(GUI_BINARY)
