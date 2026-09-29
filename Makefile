# HomeCloud build. Requires Go 1.23+ and, for the console, Node.js 20+.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X github.com/homecloudhq/homecloud/cli/cmd.Version=$(VERSION) -X github.com/homecloudhq/homecloud/cli/cmd.Commit=$(COMMIT)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
DIST := dist
WEB := cli/internal/web/dist

.PHONY: all build console test vet release clean dev

all: console build

## build: compile homecloud for this machine into ./bin/homecloud
build:
	cd cli && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/homecloud .

## console: build the web console and stage it for embedding
console:
	cd console && npm ci && npm run build
	find $(WEB) -mindepth 1 ! -name .keep -exec rm -rf {} +
	cp -R console/out/. $(WEB)/

test:
	cd cli && go test ./...

vet:
	cd cli && go vet ./...

## release: cross-compile every platform into dist/ as archives plus checksums
release: console
	rm -rf $(DIST) && mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		name=homecloud-$$os-$$arch; echo "building $$name"; \
		mkdir -p $(DIST)/$$name; \
		(cd cli && CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(DIST)/$$name/homecloud$$ext .); \
		cp LICENSE README.md $(DIST)/$$name/; \
		if [ $$os = windows ]; then (cd $(DIST) && zip -qr $$name.zip $$name); \
		else tar -C $(DIST) -czf $(DIST)/$$name.tar.gz $$name; fi; \
		rm -rf $(DIST)/$$name; \
	done
	cd $(DIST) && (command -v sha256sum >/dev/null && sha256sum * || shasum -a 256 *) > checksums.txt

## dev: run the API server with the console dev server (two terminals recommended)
dev:
	cd cli && go run . serve

clean:
	rm -rf bin $(DIST) console/out console/.next
	find $(WEB) -mindepth 1 ! -name .keep -exec rm -rf {} +
