WORKDIR := $(shell pwd)
LATEST-GIT-SHA := $(shell git rev-parse HEAD)
VERSION := $(shell cat VERSION)
FLAGS := -X main.version=$(VERSION) -X main.commit=$(LATEST-GIT-SHA)
BIN := anklet
ARCH ?= $(shell arch)
ifeq ($(ARCH), i386)
	ARCH = amd64
endif
ifeq ($(ARCH), x86_64)
	ARCH = amd64
endif
OS_TYPE ?= $(shell uname -s | tr '[:upper:]' '[:lower:]')
BIN_FULL ?= dist/$(BIN)_v$(VERSION)_$(OS_TYPE)_$(ARCH)
export PATH := $(shell go env GOPATH)/bin:$(PATH)

all: build-and-install
build-and-install: clean go.releaser install

#clean:		@ Remove the plugin binary
clean:
	rm -f docs.zip
	rm -rf dist
	rm -f ~/bin/$(BIN)

#go.releaser 	@ Run goreleaser for current version
go.releaser:
	git tag -d "$(VERSION)" 2>/dev/null || true
	git tag -a "$(VERSION)" -m "Version $(VERSION)"
	echo "LATEST TAG: $$(git describe --tags --abbrev=0)"
	goreleaser release --verbose --clean

#go.lint:		@ Run `golangci-lint run` against the current code
go.lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin v1.57.2
	golangci-lint run --fast

go.test:
	go mod tidy
	# go test -v *

install:
	mkdir -p ~/bin
	cp -f dist/$(BIN)_v$(VERSION)_$(OS_TYPE)_$(ARCH) ~/bin/$(BIN)

go.build:
	GOARCH=$(ARCH) go build $(RACE) -ldflags "-X main.version=$(VERSION)" -o docker/$(BIN)_$(OS_TYPE)_$(ARCH)
	chmod +x docker/$(BIN)_$(OS_TYPE)_$(ARCH)

build-linux:
	GOOS=linux OS_TYPE=linux $(MAKE) go.build