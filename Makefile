# Makefile for kaput-not Kubernetes controller

# Variables
BINARY_NAME=kaput-not
DOCKER_IMAGE?=ghcr.io/bsure-analytics/kaput-not
VERSION?=latest
GOOS?=$(shell go env GOOS)
GOARCH?=$(shell go env GOARCH)

# Build targets
.PHONY: all
all: test build

.PHONY: build
build:
	@echo "Building $(BINARY_NAME) for $(GOOS)/$(GOARCH)..."
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags="-w -s" -o bin/$(BINARY_NAME) ./cmd/kaput-not

.PHONY: test
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...

.PHONY: test-coverage
test-coverage: test
	@echo "Generating coverage report..."
	go tool cover -html=coverage.out -o coverage.html

.PHONY: lint
lint:
	@echo "Running linters..."
	golangci-lint run ./...

.PHONY: fmt
fmt:
	@echo "Formatting code..."
	go fmt ./...
	gofmt -s -w .

.PHONY: vet
vet:
	@echo "Running go vet..."
	go vet ./...

.PHONY: tidy
tidy:
	@echo "Tidying go modules..."
	go mod tidy

# Docker targets
.PHONY: docker-build
docker-build:
	@echo "Building Docker image $(DOCKER_IMAGE):$(VERSION)..."
	docker build -t $(DOCKER_IMAGE):$(VERSION) .

.PHONY: docker-push
docker-push: docker-build
	@echo "Pushing Docker image $(DOCKER_IMAGE):$(VERSION)..."
	docker push $(DOCKER_IMAGE):$(VERSION)

# Helm targets
CHART_PATH=charts/kaput-not
RELEASE_NAME?=kaput-not
NAMESPACE?=kube-system

.PHONY: helm-lint
helm-lint:
	@echo "Linting Helm chart..."
	helm lint $(CHART_PATH)

.PHONY: helm-template
helm-template:
	@echo "Rendering Helm templates..."
	helm template $(RELEASE_NAME) $(CHART_PATH) --namespace $(NAMESPACE)

.PHONY: helm-install
helm-install:
	@echo "Installing Helm chart..."
	helm install $(RELEASE_NAME) $(CHART_PATH) --namespace $(NAMESPACE) --create-namespace

.PHONY: helm-upgrade
helm-upgrade:
	@echo "Upgrading Helm release..."
	helm upgrade --install $(RELEASE_NAME) $(CHART_PATH) --namespace $(NAMESPACE) --create-namespace

.PHONY: helm-uninstall
helm-uninstall:
	@echo "Uninstalling Helm release..."
	helm uninstall $(RELEASE_NAME) --namespace $(NAMESPACE)

.PHONY: helm-package
helm-package:
	@echo "Packaging Helm chart..."
	helm package $(CHART_PATH) -d dist/

# Aliases for backward compatibility
.PHONY: deploy
deploy: helm-upgrade

.PHONY: undeploy
undeploy: helm-uninstall

# Cleanup
.PHONY: clean
clean:
	@echo "Cleaning up..."
	rm -rf bin/
	rm -f coverage.out coverage.html

# Development helpers
.PHONY: run
run: build
	@echo "Running $(BINARY_NAME) locally..."
	./bin/$(BINARY_NAME)

.PHONY: deps
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod verify

.PHONY: help
help:
	@echo "Available targets:"
	@echo "  all              - Run tests and build"
	@echo "  build            - Build binary"
	@echo "  test             - Run tests"
	@echo "  test-coverage    - Run tests with coverage report"
	@echo "  lint             - Run linters"
	@echo "  fmt              - Format code"
	@echo "  vet              - Run go vet"
	@echo "  tidy             - Tidy go modules"
	@echo "  docker-build     - Build Docker image"
	@echo "  docker-push      - Build and push Docker image"
	@echo "  helm-lint        - Lint Helm chart"
	@echo "  helm-template    - Render Helm templates"
	@echo "  helm-install     - Install Helm chart"
	@echo "  helm-upgrade     - Upgrade Helm release (or install if not exists)"
	@echo "  helm-uninstall   - Uninstall Helm release"
	@echo "  helm-package     - Package Helm chart"
	@echo "  deploy           - Deploy to Kubernetes (alias for helm-upgrade)"
	@echo "  undeploy         - Remove from Kubernetes (alias for helm-uninstall)"
	@echo "  clean            - Clean build artifacts"
	@echo "  run              - Build and run locally"
	@echo "  deps             - Download dependencies"
	@echo "  help             - Show this help"
