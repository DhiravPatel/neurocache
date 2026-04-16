SHELL := /usr/bin/env bash
IMAGE ?= neurocache/engine:latest

.PHONY: help install dev build docker docker-run docker-push stop logs clean test

help:
	@echo "NeuroCache — common targets"
	@echo ""
	@echo "  make install      Install locally via Docker (one-shot)"
	@echo "  make dev          Run Go API + Vite dashboard (hot reload)"
	@echo "  make build        Build everything (web + Go binary with embedded UI)"
	@echo "  make docker       Build the Docker image ($(IMAGE))"
	@echo "  make docker-run   Build + run the Docker image on :8080 / :6379"
	@echo "  make docker-push  Push the image to Docker Hub"
	@echo "  make stop         Stop the local neurocache container"
	@echo "  make logs         Tail container logs"
	@echo "  make clean        Remove build artefacts"
	@echo "  make test         Run backend + web tests"

install:
	./scripts/install.sh

dev:
	pnpm install
	pnpm dev

build:
	pnpm install
	pnpm --filter @neurocache/web build
	rm -rf apps/api/internal/webui/dist
	cp -r apps/web/dist apps/api/internal/webui/dist
	cd apps/api && go build -ldflags="-s -w" -o ../../bin/neurocache ./cmd/server
	cd apps/api && go build -ldflags="-s -w" -o ../../bin/neurocache-cli ./cmd/cli
	@echo "→ bin/neurocache (server + embedded dashboard)"
	@echo "→ bin/neurocache-cli"

docker:
	docker build -t $(IMAGE) .

docker-run: docker
	docker rm -f neurocache 2>/dev/null || true
	docker run -d --name neurocache \
	  -p 8080:8080 -p 6379:6379 \
	  -v neurocache-data:/data \
	  $(IMAGE)
	@echo "→ http://localhost:8080"

docker-push:
	docker push $(IMAGE)

stop:
	docker stop neurocache 2>/dev/null || true

logs:
	docker logs -f neurocache

clean:
	rm -rf bin data apps/api/bin
	find . -name node_modules -type d -prune -exec rm -rf {} +
	rm -rf apps/web/dist .turbo

test:
	cd apps/api && go test ./...
	pnpm --filter @neurocache/web lint
