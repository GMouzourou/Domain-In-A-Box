# Run `make help` to list the developer targets.
.DEFAULT_GOAL := help

VERSION ?= latest
IMAGE_NAME ?= domain-in-a-box
STORK_IMAGE_NAME ?= domain-in-a-box-stork
BRIDGE_IMAGE_NAME ?= domain-in-a-box-external-dns-bridge
REGISTRY ?= gmouzourou
COMPOSE ?= docker compose
BAKE = TAG="$(VERSION)" REGISTRY="$(REGISTRY)" IMAGE_NAME="$(IMAGE_NAME)" STORK_IMAGE_NAME="$(STORK_IMAGE_NAME)" BRIDGE_IMAGE_NAME="$(BRIDGE_IMAGE_NAME)" RELEASE_VERSION="$$(cat VERSION)" docker buildx bake -f docker-bake.hcl
TEST_COMPOSE = $(COMPOSE) -f tests/docker-compose.test.yml
DEV_COMPOSE = $(COMPOSE) -f docker-compose.yml

SHELL_SCRIPTS = tests/linux-client/entrypoint.sh tests/linux-client/ldap-gssapi-test.sh tests/kubernetes/lab.sh tests/kubernetes/helm-chart-test.sh
HADOLINT_IMAGE = hadolint/hadolint:v2.15.1@sha256:32dac94127fd60b7b7e3fbfc65e1383b9b5e25c9bfd7b8536de7a539fe68a12d
HADOLINT_IGNORES = --ignore DL3008 --ignore DL3015 --ignore DL3018

.PHONY: help build build-stork build-bridge build-test-client \
	test-unit test-all test-prebuilt test-clean test-verbose test-health test-dns test-dhcp test-ad test-ad-linux \
	lint version-check release-version go-lint shellcheck hadolint docker-lint helm-lint markdown-link-check \
	k8s-all k8s-up k8s-images k8s-build k8s-load k8s-deploy k8s-test k8s-logs k8s-down \
	dev-up dev-down dev-logs dev-exec clean

##@ General
help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^##@/ {printf "\n%s:\n", substr($$0, 5)} /^[a-zA-Z0-9_-]+:.*## / {printf "  make %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo ""
	@echo "Example: make dev-up && make test-all && make dev-down"

##@ Build
build: ## Build the core image
	$(BAKE) --load core

build-stork: ## Build the Stork server image
	$(BAKE) --load stork

build-bridge: ## Build the optional ExternalDNS bridge image
	$(BAKE) --load external-dns-bridge

build-test-client: ## Build the Linux test client image
	$(BAKE) --load test-client

##@ Test
test-unit: ## Run the Go unit tests for the product and the test runner
	go test ./...
	cd tests/test-runner && go test ./...

test-all: test-clean ## Build the images and run the Compose integration suite
	$(TEST_COMPOSE) up --build --abort-on-container-exit --exit-code-from test-runner

test-prebuilt: test-clean ## Run the Compose integration suite with already-built images
	$(TEST_COMPOSE) up --abort-on-container-exit --exit-code-from test-runner

test-clean: ## Remove the integration test containers and volumes
	$(TEST_COMPOSE) down -v

test-verbose: ## Run every test-runner check against a running stack, verbosely
	cd tests/test-runner && go run . run all --verbose

test-health: ## Run the health checks against a running stack
	cd tests/test-runner && go run . run health

test-dns: ## Run the DNS checks against a running stack
	cd tests/test-runner && go run . run dns

test-dhcp: ## Run the DHCP checks against a running stack
	cd tests/test-runner && go run . run dhcp

test-ad: ## Run the Active Directory checks against a running stack
	cd tests/test-runner && go run . run ad

test-ad-linux: ## Run the Linux AD join checks against a running stack
	cd tests/test-runner && go run . run ad-linux

##@ Lint
lint: version-check go-lint shellcheck hadolint docker-lint helm-lint markdown-link-check ## Run every lint check

version-check: ## Check that release versions and pins agree
	go run ./cmd/dib-release-ctl check

release-version: ## Update every release pin: make release-version RELEASE_VERSION=X.Y.Z
	go run ./cmd/dib-release-ctl update $(RELEASE_VERSION)

go-lint: ## Lint and format-check both Go modules with golangci-lint
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint is required" >&2; exit 1; }
	golangci-lint run ./...
	cd tests/test-runner && golangci-lint run ./...

shellcheck: ## Lint the shell scripts
	@command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck is required" >&2; exit 1; }
	shellcheck -x $(SHELL_SCRIPTS)

hadolint: ## Lint the Dockerfiles with a pinned hadolint image
	docker run --rm -i $(HADOLINT_IMAGE) hadolint $(HADOLINT_IGNORES) - < Dockerfile
	docker run --rm -i $(HADOLINT_IMAGE) hadolint $(HADOLINT_IGNORES) - < tests/linux-client/Dockerfile

docker-lint: ## Validate the Compose files
	$(DEV_COMPOSE) config > /dev/null
	$(TEST_COMPOSE) config > /dev/null

helm-lint: ## Lint, render and test both Helm charts
	@command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
	helm lint charts/domain-in-a-box
	helm template dib charts/domain-in-a-box > /dev/null
	helm template dib charts/domain-in-a-box -f tests/kubernetes/values.ci.yaml > /dev/null
	bash tests/kubernetes/helm-chart-test.sh
	helm lint charts/domain-in-a-box-external-dns-bridge -f tests/dnsbridge/values.ci.yaml
	helm template dns charts/domain-in-a-box-external-dns-bridge -f tests/dnsbridge/values.ci.yaml > /dev/null

markdown-link-check: ## Check Markdown links offline
	@command -v lychee >/dev/null 2>&1 || { echo "lychee is required" >&2; exit 1; }
	lychee --offline --include-fragments README.md docs/*.md

##@ Kubernetes lab (kind + Multus/macvlan)
k8s-all: k8s-up k8s-images k8s-deploy k8s-test ## Provision, deploy and test end to end

k8s-up: ## Create the kind cluster with Multus
	tests/kubernetes/lab.sh up

k8s-images: ## Build and load the images into the cluster
	tests/kubernetes/lab.sh images

k8s-build: ## Build the lab images only
	tests/kubernetes/lab.sh build

k8s-load: ## Load already-built images into the cluster
	tests/kubernetes/lab.sh load

k8s-deploy: ## Install the Helm chart
	tests/kubernetes/lab.sh deploy

k8s-test: ## Run the test suite in the cluster
	tests/kubernetes/lab.sh test

k8s-logs: ## Collect cluster diagnostics
	tests/kubernetes/lab.sh logs

k8s-down: ## Delete the cluster and lab network
	tests/kubernetes/lab.sh down

##@ Local development
dev-up: ## Build and start the Compose stack from .env
	$(DEV_COMPOSE) up --build -d

dev-down: ## Stop the Compose stack and DELETE its volumes
	$(DEV_COMPOSE) down -v

dev-logs: ## Follow the Compose stack logs
	$(DEV_COMPOSE) logs -f

dev-exec: ## Open a shell in the running controller
	$(DEV_COMPOSE) exec domain-controller bash

clean: ## Remove the development and test stacks and their volumes
	-$(DEV_COMPOSE) down -v
	-$(TEST_COMPOSE) down -v
