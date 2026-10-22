.PHONY: web build dev test site release vm-test clean

# The dashboard is embedded into the binary, so build it first.
web:
	cd web && npm ci && npm run build

build: web
	go build -o sideyard ./cmd/sideyard

# Dashboard at http://sideyard.localhost:8080, apps at http://<name>.localhost:8080.
dev: build
	SIDEYARD_DEV=1 ./sideyard

test:
	go vet ./...
	go test -race ./...
	cd web && npm run typecheck
	scripts/prompts.sh --check

# The landing page (site/) is static. Its agent prompts embed the starter, so
# rebuild them when templates/starter or scripts/prompts/ changes.
site:
	scripts/prompts.sh

# A release in dist/: Linux binaries, release.json, SHA256SUMS and install.sh.
# Pushing a tag (git tag v1.2.3 && git push --tags) makes the GitHub workflow
# run this and publish it. SOURCE is where it's published.
VERSION ?= v0.0.0-dev
SOURCE ?=
release: web
	VERSION=$(VERSION) SOURCE=$(SOURCE) scripts/release.sh

# Systemd runner test for a Linux VM; see TestSystemdRunner for how to run it.
vm-test:
	mkdir -p dist/vm-test
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go test -c -o dist/vm-test/platform-$$arch.test ./internal/platform && \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -o dist/vm-test/testapp-$$arch ./internal/platform/testdata/app || exit 1; \
	done

clean:
	rm -rf sideyard dist .sideyard-data
