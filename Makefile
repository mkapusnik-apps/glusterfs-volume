PLUGIN ?= glusterfs-volume
REGISTRY ?=
CONTEXT ?= default
DOCKER ?= docker
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf dev)
VCS_REF ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)

PLUGIN_NAME := $(REGISTRY)$(PLUGIN)
GO_SOURCES := $(shell find . -type f -name '*.go' -print)

.PHONY: all build image plugin clean push

all: build

bin/linux/docker-volume-glusterfs: $(GO_SOURCES)
	@echo "[MAKE] Building glusterfs binary"
	@mkdir -p bin/linux
	@DOCKER_BUILDKIT=1 \
	  $(DOCKER) --context $(CONTEXT) build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg VCS_REF=$(VCS_REF) \
	  --target artifact \
	  -o type=local,dest=bin/linux .

image: bin/linux/docker-volume-glusterfs
	@echo "[MAKE] Building plugin image $(PLUGIN_NAME)"
	@DOCKER_BUILDKIT=1 $(DOCKER) --context $(CONTEXT) build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg VCS_REF=$(VCS_REF) \
	  -t $(PLUGIN_NAME) .

plugin: image
	@echo "[MAKE] Creating rootfs"
	@rm -rf plugin/rootfs
	@mkdir -p plugin/rootfs
	@$(DOCKER) --context $(CONTEXT) create $(PLUGIN_NAME) > plugin/container.id
	@$(DOCKER) --context $(CONTEXT) export "$$(cat plugin/container.id)" | tar -x -C plugin/rootfs
	@$(DOCKER) --context $(CONTEXT) rm -f "$$(cat plugin/container.id)"
	@cp config.json plugin/

build: plugin
	@echo "[MAKE] Creating docker volume plugin $(PLUGIN_NAME)"
	@if $(DOCKER) --context $(CONTEXT) plugin inspect $(PLUGIN_NAME) >/dev/null 2>&1; then \
		$(DOCKER) --context $(CONTEXT) plugin disable --force $(PLUGIN_NAME); \
		$(DOCKER) --context $(CONTEXT) plugin rm --force $(PLUGIN_NAME); \
	fi
	@$(DOCKER) --context $(CONTEXT) plugin create $(PLUGIN_NAME) ./plugin

push: build
	@echo "[MAKE] Pushing plugin $(PLUGIN_NAME)"
	@$(DOCKER) --context $(CONTEXT) plugin push $(PLUGIN_NAME)

clean:
	@rm -rf plugin/rootfs plugin/container.id bin/linux
