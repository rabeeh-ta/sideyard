# Local development:   make api   (Go on :3000)   and   make web-dev   (Vite on :5173)
# Deploy from laptop:  put SIDEYARD_URL/PROJECT/TOKEN in .env.deploy, then make deploy

GOARCH ?= amd64

.PHONY: api web-dev web build deploy

api:
	go run .

web-dev:
	cd web && npm run dev

web:
	cd web && npm ci && npm run build

# A Linux binary for the server, with the frontend embedded.
build: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -ldflags="-s -w" -o app .

deploy: build
	@set -a; [ -f .env.deploy ] && . ./.env.deploy; set +a; ./scripts/deploy.sh app
