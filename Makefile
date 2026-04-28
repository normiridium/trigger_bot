GO ?= /usr/local/go/bin/go
GOFMT ?= /usr/local/go/bin/gofmt
APP_BIN ?= trigger_admin_bot
PKGS ?= ./...

.PHONY: fmt test build

fmt:
	@files="$$(find . -type f -name '*.go' -not -path './vendor/*')"; \
	if [ -n "$$files" ]; then \
		$(GOFMT) -w $$files; \
	fi

test:
	$(GO) test $(PKGS)

build:
	$(GO) build -o $(APP_BIN) .
