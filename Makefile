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

#clean:		@ Remove the build binaries
clean:
	rm -rf dist
	rm -f ~/bin/$(BIN)

#go.releaser 	@ Run goreleaser for current version
go.releaser:
	git tag -d "$(VERSION)" 2>/dev/null || true
	git tag -a "$(VERSION)" -m "Version $(VERSION)"
	echo "LATEST TAG: $$(git describe --tags --abbrev=0)"
	goreleaser release --verbose --clean
	file dist/* | grep executable | awk '{print $1}' | cut -d: -f1 | xargs rm -f

#go.lint:		@ Run `golangci-lint run` against the current code
go.lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $(shell go env GOPATH)/bin v2.1.6
	golangci-lint run

go.test:
	go mod tidy
	# go test -v *

install:
	mkdir -p ~/bin
	cp -f dist/$(BIN)_v$(VERSION)_$(OS_TYPE)_$(ARCH) ~/bin/$(BIN)

go.build:
	GOARCH=$(ARCH) go build $(RACE) -ldflags "-X main.version=$(VERSION)" -o dist/$(BIN)_v$(VERSION)_$(OS_TYPE)_$(ARCH)
	chmod +x dist/$(BIN)_v$(VERSION)_$(OS_TYPE)_$(ARCH)

build-linux:
	GOOS=linux OS_TYPE=linux $(MAKE) go.build

run-docker-compose:
	cp dist/anklet_v$(VERSION)*_linux_$(ARCH) docker/
	cd docker && \
		rm -f anklet_linux_$(ARCH) && \
		mv anklet_v$(VERSION)*_linux_$(ARCH) anklet_linux_$(ARCH) && \
		rm -f anklet_v$(VERSION)*_linux_$(ARCH).zip && \
		docker-compose up --build --force-recreate
