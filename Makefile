GO ?= /usr/local/go/bin/go
GOFMT ?= /usr/local/go/bin/gofmt
APP_BIN ?= trigger_admin_bot
PKGS ?= ./...
GO_LIMIT_PROCS ?= 1
GO_BUILD_P ?= 1

.PHONY: fmt test build preflight build-safe test-safe

fmt:
	@files="$$(find . -type f -name '*.go' -not -path './vendor/*')"; \
	if [ -n "$$files" ]; then \
		$(GOFMT) -w $$files; \
	fi

test:
	$(GO) test $(PKGS)

build:
	$(GO) build -o $(APP_BIN) .

preflight:
	@echo "== preflight ==" && free -h && echo && df -h && echo && uptime

build-safe: preflight
	GOMAXPROCS=$(GO_LIMIT_PROCS) $(GO) build -p $(GO_BUILD_P) -o $(APP_BIN) .

test-safe: preflight
	GOMAXPROCS=$(GO_LIMIT_PROCS) $(GO) test -p $(GO_BUILD_P) $(PKGS)
