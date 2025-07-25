CMD=wsstat
PACKAGE_NAME=./cmd/${CMD}
OS_ARCH_PAIRS=linux/386 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/386 windows/amd64 windows/arm64
VERSION := $(shell cat VERSION)
LDFLAGS=-ldflags "-X main.version=${VERSION}"

.PHONY: build build-all build-multi build-os-arch explain lint test

.DEFAULT_GOAL := explain

explain:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Options for test targets:"
	@echo "  [N=...]  - Number of times to run burst tests (default 1)"
	@echo "  [RACE=1] - Run tests with race detector"
	@echo "  [V=1]    - Add V=1 for verbose output"
	@echo ""
	@echo "Targets:"
	@echo "  build           - Build the binary for the host OS/Arch."
	@echo "  build-all       - Build binaries for all target OS/Arch pairs."
	@echo "  lint            - Run linter (golangci-lint)."
	@echo "  test            - Run tests."
	@echo "  explain         - Display this help message."

# Number of times to run burst tests, default 1
N ?= 1

TEST_FLAGS :=
ifdef RACE
	TEST_FLAGS += -race
endif
ifdef V
	TEST_FLAGS += -v
endif

build:
	@echo "==> Building binary..."
	go build ${LDFLAGS} -o bin/${CMD} ${PACKAGE_NAME}

build-all: TARGETS=$(OS_ARCH_PAIRS)
build-all: build-multi

build-multi:
	@echo "==> Building binaries for all target OS/Arch pairs..."
	$(foreach PAIR,$(TARGETS), $(MAKE) --no-print-directory build-os-arch OS_ARCH=$(PAIR);)

build-os-arch:
	@GOOS=$(firstword $(subst /, ,$(OS_ARCH))) \
	GOARCH=$(lastword $(subst /, ,$(OS_ARCH))) \
	go build ${LDFLAGS} -o 'bin/$(CMD)-$(firstword $(subst /, ,$(OS_ARCH)))-$(lastword $(subst /, ,$(OS_ARCH)))' ${PACKAGE_NAME}

lint:
	@echo "==> Running linter (golangci-lint)..."
	@golangci-lint run ./... || echo "Linting failed or golangci-lint not found. Consider installing it: https://golangci-lint.run/usage/install/"

test:
	@echo "==> Running tests..."
	@go test -count=$(N) $(TEST_FLAGS) ./...
