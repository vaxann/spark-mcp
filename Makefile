BIN := spark-mcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX ?= $(HOME)/.local
AGENT_LABEL := io.github.vaxann.spark-mcp
AGENT_PLIST := $(HOME)/Library/LaunchAgents/$(AGENT_LABEL).plist

.PHONY: build test race lint vet vuln tidy fmt install install-agent uninstall-agent

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/$(BIN)

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...

tidy:
	go mod tidy

install: build
	install -d $(PREFIX)/bin
	install -m 0755 bin/$(BIN) $(PREFIX)/bin/$(BIN)

# Installs a LaunchAgent from deploy/launchd/spark-mcp.local.plist (copy the
# template there and fill in the token and password first; it is git-ignored).
install-agent: install
	@test -f deploy/launchd/spark-mcp.local.plist || { echo "copy deploy/launchd/spark-mcp.plist to deploy/launchd/spark-mcp.local.plist and fill it in"; exit 1; }
	install -d $(HOME)/Library/LaunchAgents $(HOME)/Library/Logs/spark-mcp
	sed -e 's#__BIN__#$(PREFIX)/bin/$(BIN)#' -e 's#__HOME__#$(HOME)#g' deploy/launchd/spark-mcp.local.plist > $(AGENT_PLIST)
	chmod 600 $(AGENT_PLIST)
	launchctl bootout gui/$$(id -u) $(AGENT_PLIST) 2>/dev/null || true
	launchctl bootstrap gui/$$(id -u) $(AGENT_PLIST)
	@echo "started $(AGENT_LABEL); logs in ~/Library/Logs/spark-mcp"

uninstall-agent:
	launchctl bootout gui/$$(id -u) $(AGENT_PLIST) 2>/dev/null || true
	rm -f $(AGENT_PLIST)
