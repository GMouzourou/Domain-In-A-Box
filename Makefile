.PHONY: help build test test-unit test-dns test-dhcp test-ad test-ad-linux test-all test-verbose test-clean lint shellcheck hadolint docker-lint push push-docker push-ghcr dev-up dev-down dev-logs clean

# Default target
help:
	@echo "Domain-In-A-Box Development Commands"
	@echo ""
	@echo "Build & Image:"
	@echo "  make build                 Build Docker image locally"
	@echo "  make build-test-client     Build test client image"
	@echo ""
	@echo "Testing:"
	@echo "  make test-all              Run all tests with docker-compose"
	@echo "  make test-unit             Run Go unit tests for the test runner"
	@echo "  make test-verbose          Run tests with verbose output"
	@echo "  make test-health           Run health check tests"
	@echo "  make test-dns              Run DNS resolution tests"
	@echo "  make test-dhcp             Run DHCP functionality tests"
	@echo "  make test-ad               Run Active Directory tests"
	@echo "  make test-ad-linux         Run Linux AD join tests"
	@echo ""
	@echo "Local Development:"
	@echo "  make dev-up                Start domain controller for development"
	@echo "  make dev-down              Stop development environment"
	@echo "  make dev-logs              View development logs"
	@echo "  make dev-exec              Execute bash in running container"
	@echo ""
	@echo "Linting & Validation:"
	@echo "  make lint                  Run all linting checks"
	@echo "  make shellcheck            Lint shell scripts"
	@echo "  make hadolint              Lint Dockerfile"
	@echo "  make docker-lint           Validate docker-compose files"
	@echo ""
	@echo "Cleanup:"
	@echo "  make test-clean            Clean up test environment"
	@echo "  make clean                 Remove all containers and volumes"
	@echo ""
	@echo "Publishing:"
	@echo "  make push-docker           Push to Docker Hub (requires secrets)"
	@echo "  make push-ghcr             Push to GitHub Container Registry"
	@echo ""
	@echo "Examples:"
	@echo "  # Run all tests"
	@echo "  make test-all"
	@echo ""
	@echo "  # Start dev environment and run tests"
	@echo "  make dev-up && make test-all && make dev-down"
	@echo ""
	@echo "  # Run specific test"
	@echo "  make test-dns VERBOSE=true"

# Image version
VERSION ?= latest
IMAGE_NAME ?= domain-in-a-box
REGISTRY ?= gmouzourou
COMPOSE ?= docker compose

# Build targets
build:
	@echo "Building Domain-In-A-Box image..."
	docker build -t $(REGISTRY)/$(IMAGE_NAME):$(VERSION) .
	@echo "✓ Build complete: $(REGISTRY)/$(IMAGE_NAME):$(VERSION)"

build-test-client:
	@echo "Building test client image..."
	docker build -t $(REGISTRY)/$(IMAGE_NAME)-test-client:$(VERSION) -f tests/linux-client/Dockerfile .
	@echo "✓ Build complete: $(REGISTRY)/$(IMAGE_NAME)-test-client:$(VERSION)"

# Test targets
test-all: test-clean
	@echo "Starting integration tests..."
	$(COMPOSE) -f tests/docker-compose.test.yml up --build --abort-on-container-exit --exit-code-from test-runner
	@echo "✓ Tests complete"

test-clean:
	@echo "Cleaning up test environment..."
	$(COMPOSE) -f tests/docker-compose.test.yml down -v
	@echo "✓ Test environment cleaned"

test-unit:
	@echo "Running Go unit tests..."
	cd tests/test-runner && go test ./...
	@echo "✓ Unit tests complete"

test-verbose: VERBOSE=true
test-verbose:
	@echo "Running tests with verbose output..."
	cd tests/test-runner && go run . run all --verbose
	@echo "✓ Verbose tests complete"

test-health:
	@echo "Running health checks..."
	cd tests/test-runner && go run . run health

test-dns:
	@echo "Running DNS tests..."
	cd tests/test-runner && go run . run dns

test-dhcp:
	@echo "Running DHCP tests..."
	cd tests/test-runner && go run . run dhcp

test-ad:
	@echo "Running Active Directory tests..."
	cd tests/test-runner && go run . run ad

test-ad-linux:
	@echo "Running Linux AD join tests..."
	cd tests/test-runner && go run . run ad-linux

# Linting targets
lint: shellcheck hadolint docker-lint
	@echo "✓ All linting checks passed"

shellcheck:
	@echo "Linting shell scripts..."
	@if command -v shellcheck &> /dev/null; then \
		shellcheck -x entrypoint.sh entrypoint.d/*.sh tests/linux-client/entrypoint.sh; \
		echo "✓ Shell scripts checked"; \
	else \
		echo "⚠ shellcheck not available"; \
	fi

hadolint:
	@echo "Linting Dockerfile..."
	@if command -v hadolint &> /dev/null; then \
		hadolint Dockerfile || true; \
		echo "✓ Dockerfile checked"; \
	else \
		echo "⚠ hadolint not available"; \
	fi

docker-lint:
	@echo "Validating docker-compose files..."
	$(COMPOSE) -f docker-compose.yml config > /dev/null
	$(COMPOSE) -f tests/docker-compose.test.yml config > /dev/null
	@echo "✓ Docker Compose files valid"

# Development targets
dev-up:
	@echo "Starting development environment..."
	$(COMPOSE) -f docker-compose.yml up --build -d
	@echo "✓ Development environment started"
	@echo ""
	@echo "Services available at:"
	@echo "  DNS:     192.168.1.1:53"
	@echo "  LDAP:    192.168.1.1:389"
	@echo "  Kerberos: 192.168.1.1:88"
	@echo "  SMB:     192.168.1.1:445"
	@echo ""
	@echo "Run 'make dev-logs' to see logs"
	@echo "Run 'make dev-down' to stop"

dev-down:
	@echo "Stopping development environment..."
	$(COMPOSE) -f docker-compose.yml down -v
	@echo "✓ Development environment stopped"

dev-logs:
	$(COMPOSE) -f docker-compose.yml logs -f

dev-exec:
	@echo "Entering development container..."
	$(COMPOSE) -f docker-compose.yml exec domain-controller bash

dev-shell:
	@echo "Opening shell in test runner..."
	$(COMPOSE) -f tests/docker-compose.test.yml exec test-runner bash

# Cleanup targets
clean:
	@echo "Removing containers and volumes..."
	$(COMPOSE) -f docker-compose.yml down -v 2>/dev/null || true
	$(COMPOSE) -f tests/docker-compose.test.yml down -v 2>/dev/null || true
	@echo "✓ Cleanup complete"

# Publishing targets
push-docker:
	@echo "Pushing to Docker Hub..."
	@if [ -z "$(DOCKER_USERNAME)" ] || [ -z "$(DOCKER_PASSWORD)" ]; then \
		echo "Error: DOCKER_USERNAME and DOCKER_PASSWORD required"; \
		exit 1; \
	fi
	docker tag $(REGISTRY)/$(IMAGE_NAME):$(VERSION) $(REGISTRY)/$(IMAGE_NAME):latest
	docker push $(REGISTRY)/$(IMAGE_NAME):$(VERSION)
	docker push $(REGISTRY)/$(IMAGE_NAME):latest
	@echo "✓ Pushed to Docker Hub"

push-ghcr:
	@echo "Pushing to GitHub Container Registry..."
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "Error: GITHUB_TOKEN required"; \
		exit 1; \
	fi
	docker tag $(REGISTRY)/$(IMAGE_NAME):$(VERSION) ghcr.io/$(REGISTRY)/$(IMAGE_NAME):$(VERSION)
	docker push ghcr.io/$(REGISTRY)/$(IMAGE_NAME):$(VERSION)
	@echo "✓ Pushed to GitHub Container Registry"

# Utility targets
docker-prune:
	@echo "Pruning Docker resources..."
	docker system prune -f
	@echo "✓ Docker resources pruned"

.DEFAULT_GOAL := help
