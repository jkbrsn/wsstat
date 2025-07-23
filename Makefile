CMD=wsstat
PACKAGE_NAME=./cmd/${CMD}
OS_ARCH_PAIRS=linux/386 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/386 windows/amd64 windows/arm64
VERSION := $(shell cat VERSION)
LDFLAGS=-ldflags "-X main.version=${VERSION}"

.PHONY: build build-all build-snapcraft build-multi build-os-arch explain

.DEFAULT_GOAL := explain

explain:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build           - Build the binary for the host OS/Arch."
	@echo "  build-all       - Build binaries for all target OS/Arch pairs."
	@echo "  explain         - Display this help message."

build:
	go build ${LDFLAGS} -o bin/${CMD} ${PACKAGE_NAME}

build-all: TARGETS=$(OS_ARCH_PAIRS)
build-all: build-multi

build-multi:
	$(foreach PAIR,$(TARGETS), $(MAKE) --no-print-directory build-os-arch OS_ARCH=$(PAIR);)

build-os-arch:
	@GOOS=$(firstword $(subst /, ,$(OS_ARCH))) \
	GOARCH=$(lastword $(subst /, ,$(OS_ARCH))) \
	go build ${LDFLAGS} -o 'bin/$(CMD)-$(firstword $(subst /, ,$(OS_ARCH)))-$(lastword $(subst /, ,$(OS_ARCH)))' ${PACKAGE_NAME}
