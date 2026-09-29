# Image URL to use all building/pushing image targets
IMG ?= controller:latest
SUPERSET_IMG ?= data-platform-superset:5.0.0

ROOT ?= $(abspath ..)
CONTROLLER_GEN ?= $(ROOT)/bin/controller-gen
ENVTEST ?= $(ROOT)/bin/setup-envtest
ENVTEST_K8S_VERSION ?= 1.36

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

.PHONY: manifests
manifests: controller-gen
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen
	"$(CONTROLLER_GEN)" object:headerFile="$(ROOT)/hack/boilerplate.go.txt",year=2026 paths="./..."

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(ROOT)/bin" -p path)" go test ./... -coverprofile cover.out

.PHONY: build
build: manifests generate fmt vet
	go build -o bin/manager ./cmd

.PHONY: run
run: manifests generate fmt vet
	go run ./cmd

.PHONY: docker-build
docker-build:
	docker build -t ${IMG} .

CONTAINER_TOOL ?= docker

.PHONY: docker-build-superset
docker-build-superset:
	$(CONTAINER_TOOL) build -t ${SUPERSET_IMG} -f images/superset/Dockerfile images/superset

.PHONY: install
install: manifests
	kubectl apply -f config/crd/bases

.PHONY: helm-sync
helm-sync: manifests
	"$(ROOT)/hack/sync-helm-manifests.sh" config/crd/bases config/rbac/role.yaml "$(ROOT)/deploy/data-analytics-operator"

.PHONY: controller-gen
controller-gen:
	$(MAKE) -C "$(ROOT)" controller-gen

.PHONY: setup-envtest
setup-envtest:
	$(MAKE) -C "$(ROOT)" setup-envtest ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION)
