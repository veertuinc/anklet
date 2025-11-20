WORKDIR := $(shell pwd)
LATEST-GIT-SHA := $(shell git rev-parse HEAD)
VERSION := $(shell cat VERSION)
FLAGS := -X main.version=$(VERSION) -X main.commit=$(LATEST-GIT-SHA)
BIN := anklet
IMAGE := veertu/anklet
TAG := latest
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
	go vet ./...
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $$(go env GOPATH)/bin latest
	echo "golangci-lint run"
	golangci-lint run

go.test:
	go mod tidy
	# go test -v *

#cross-compile-check:	@ Check compilation for all target platforms without building
cross-compile-check:
	@echo "Checking compilation for all target platforms..."
	@echo "Using goreleaser to test all platform builds..."
	goreleaser build --snapshot --clean --single-target=false --skip-validate
	@echo "✅ All target platforms compile successfully via goreleaser"

#quick-compile-check:	@ Quick check for obvious cross-platform issues
quick-compile-check:
	@echo "Quick cross-platform syntax check (individual packages)..."
	@for pkg in $$(go list ./internal/... | grep -v internal/host); do \
		echo "Checking $$pkg..."; \
		GOOS=linux GOARCH=amd64 go build -o /dev/null $$pkg || exit 1; \
	done
	@echo "✅ Internal packages (except host) compile for Linux"

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

#build-snapshot:	@ Build snapshot binaries with goreleaser
build-snapshot:
	@echo "Building snapshot with goreleaser..."
	goreleaser build --snapshot --clean

#prepare-docker:	@ Copy Linux binaries to docker folder and build multi-arch image
prepare-docker:
	@echo "Copying Linux binaries to docker folder..."
	cp dist/anklet_v*-SNAPSHOT-*_linux_amd64 docker/anklet_linux_amd64
	cp dist/anklet_v*-SNAPSHOT-*_linux_arm64 docker/anklet_linux_arm64
	@echo "Building multi-arch Docker image..."
	cd docker && docker buildx build --platform linux/amd64,linux/arm64 -t anklet:snapshot .

#push-docker:	@ Push Docker image to registry
push-docker:
	@echo "Pushing multi-arch Docker image to registry..."
	docker tag anklet:snapshot $(IMAGE):$(TAG)
	docker push $(IMAGE):$(TAG)

#build-docker-snapshot:	@ Complete workflow: build snapshot, prepare docker, and build image
build-docker-snapshot: build-snapshot prepare-docker

#build-docker-snapshot-push:	@ Complete workflow: build, prepare, and push to registry
build-docker-snapshot-push: build-snapshot prepare-docker push-docker

#load-docker:	@ Build and load Docker image for local testing
load-docker:
	@echo "Building and loading Docker image for local use..."
	cd docker && docker buildx build --platform linux/amd64,linux/arm64 -t anklet:snapshot --load .
