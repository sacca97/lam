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

# The build with the window (written to lam-gui, installed as lam) links Gio's Wayland and X11 backends. Without the
# xkbcommon-x11 development files it is built for Wayland only.
GUI_BINARY := lam-gui
GUI_TAGS   ?= gui$(shell pkg-config --exists xkbcommon-x11 2>/dev/null || echo ,nox11)
DESKTOP_DIR ?= $(HOME)/.local/share/applications
ICON_DIR    ?= $(HOME)/.local/share/icons
ICON_SIZES  := 16 32 48 64 128 256 512
SIGN_APP    ?= 1
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

install: ## Install lam: lam.app and a lam command pointing into it on macOS; lam with its window on Linux (terminal-only if that does not build)
	@set -e; \
	install -d "$(BINDIR)"; \
	if [ "$$(uname -s)" = Darwin ]; then \
		$(MAKE) --no-print-directory gui; \
		$(MAKE) --no-print-directory macapp; \
		rm -f "$(BINDIR)/$(BINARY)"; \
		ln -s "$(APP_BUNDLE)/Contents/MacOS/lam" "$(BINDIR)/$(BINARY)"; \
		echo "installed $(BINDIR)/$(BINARY) -> $(APP_BUNDLE)/Contents/MacOS/lam"; \
	elif $(MAKE) --no-print-directory gui >/dev/null 2>&1; then \
		install -m 755 "$(GUI_BINARY)" "$(BINDIR)/$(BINARY)"; \
		echo "installed $(BINDIR)/$(BINARY) ($(GUI_TAGS))"; \
		install -d "$(DESKTOP_DIR)"; \
		printf '[Desktop Entry]\nType=Application\nName=lam\nComment=Agent sessions\nExec=%s gui\nIcon=lam\nTerminal=false\nCategories=Development;\n' \
			"$(BINDIR)/$(BINARY)" > "$(DESKTOP_DIR)/lam.desktop"; \
		for s in $(ICON_SIZES); do \
			install -d "$(ICON_DIR)/hicolor/$${s}x$${s}/apps"; \
			install -m 644 assets/icon/png/lam-$$s.png "$(ICON_DIR)/hicolor/$${s}x$${s}/apps/lam.png"; \
		done; \
		gtk-update-icon-cache -q -t "$(ICON_DIR)/hicolor" 2>/dev/null || true; \
	else \
		$(MAKE) --no-print-directory build; \
		install -m 755 "$(BINARY)" "$(BINDIR)/$(BINARY)"; \
		echo "installed $(BINDIR)/$(BINARY) without the window (run 'make gui' to see why it did not build)"; \
	fi; \
	rm -f "$(BINDIR)/$(GUI_BINARY)"; \
	ln -sf "$(BINARY)" "$(BINDIR)/lam-tui"
	@# Only refresh the service that runs this install: an install elsewhere
	@# (another PREFIX) must not repoint it.
	@if grep -qF "$(BINDIR)/$(BINARY) daemon" $(HOME)/.config/systemd/user/lam-daemon.service 2>/dev/null; then \
		$(BINDIR)/$(BINARY) install-service && systemctl --user restart lam-daemon.service; \
	fi
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "warning: $(BINDIR) is not in PATH";; esac

# macapp makes lam.app so Launchpad and Spotlight find the window. Its
# executable is lam itself, which takes the login shell's environment when
# started from Finder (see login_env.go).
macapp:
	rm -rf "$(APP_BUNDLE)"
	install -d "$(APP_BUNDLE)/Contents/MacOS"
	install -m 755 "$(GUI_BINARY)" "$(APP_BUNDLE)/Contents/MacOS/lam"
	install -d "$(APP_BUNDLE)/Contents/Resources"
	install -m 644 assets/icon/lam.icns "$(APP_BUNDLE)/Contents/Resources/lam.icns"
	printf '<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict>\n<key>CFBundleName</key><string>lam</string>\n<key>CFBundleIdentifier</key><string>io.github.sacca97.lam</string>\n<key>CFBundleExecutable</key><string>lam</string>\n<key>CFBundleIconFile</key><string>lam</string>\n<key>CFBundlePackageType</key><string>APPL</string>\n<key>CFBundleVersion</key><string>1</string>\n<key>CFBundleShortVersionString</key><string>$(patsubst v%,%,$(VERSION))</string>\n<key>LSMinimumSystemVersion</key><string>11.0</string>\n<key>NSHighResolutionCapable</key><true/>\n<key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>\n</dict></plist>\n' > "$(APP_BUNDLE)/Contents/Info.plist"
	@# Notifications need the bundle to be signed; an ad-hoc signature does. A
	@# cross-build (SIGN_APP=0) is signed afterwards by scripts/cross-darwin-gui.sh.
	@if [ "$(SIGN_APP)" = 0 ]; then :; elif command -v codesign >/dev/null 2>&1; then \
		codesign --force --deep -s - "$(APP_BUNDLE)" && echo "signed $(APP_BUNDLE) (ad hoc)" || echo "warning: could not sign $(APP_BUNDLE); notifications will not show"; \
	else echo "warning: codesign not found; $(APP_BUNDLE) is unsigned and notifications will not show"; fi
	@echo "installed $(APP_BUNDLE)"

cross-mac-gui: ## Build lam.app for macOS arm64 from Linux: make cross-mac-gui MACOS_SDK=/path/to/MacOSX.sdk
	@scripts/cross-darwin-gui.sh

release: ## Build the release archives and prepare the tag: make release TAG=v0.2.0
	@scripts/release.sh $(TAG)

uninstall: ## Remove the installed binaries
	rm -f "$(BINDIR)/$(BINARY)" "$(BINDIR)/$(GUI_BINARY)" "$(BINDIR)/lam-tui" "$(DESKTOP_DIR)/lam.desktop"
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
