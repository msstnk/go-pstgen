# Developer build helpers. End users: go install github.com/msstnk/go-pstgen/cmd/go-pstgen@latest
CMDS    := go-pstgen go-pstprune
ARCHS   ?= amd64
OSES    := linux windows
VERSION := $(shell sed -n 's/^const version = "\(.*\)"/\1/p' cmd/go-pstgen/version.go)
# The commit id and dirty flag are embedded by the Go toolchain (-buildvcs) and read via debug.ReadBuildInfo.
GOBUILD := CGO_ENABLED=0 go build

.DEFAULT_GOAL := help
.PHONY: help build build-all clean

help:
	@echo "Targets:"
	@echo "  make build      build for this machine into bin/ (version $(VERSION))"
	@echo "  make build-all  build for OSES='$(OSES)' x ARCHS='$(ARCHS)' into bin/<cmd>-$(VERSION)-<os>-<arch>[.exe]"
	@echo "  make clean      remove bin/"

build:
	@for c in $(CMDS); do echo "building $$c ($$(go env GOOS)/$$(go env GOARCH))"; $(GOBUILD) -o bin/$$c ./cmd/$$c || exit 1; done

build-all:
	@for os in $(OSES); do for arch in $(ARCHS); do \
	  ext=; [ $$os = windows ] && ext=.exe; \
	  for c in $(CMDS); do \
	    echo "building $$c $$os/$$arch -> bin/$$c-$(VERSION)-$$os-$$arch$$ext"; \
	    GOOS=$$os GOARCH=$$arch $(GOBUILD) -o bin/$$c-$(VERSION)-$$os-$$arch$$ext ./cmd/$$c || exit 1; \
	  done; \
	done; done

clean:
	rm -rf bin
