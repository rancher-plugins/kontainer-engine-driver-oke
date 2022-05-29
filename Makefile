NAME:=kontainer-engine-driver-oke

# local build, use user and timestamp it
BINARY_NAME ?= ${NAME}
VERSION:=$(shell  date +%Y%m%d%H%M%S)

DIST_DIR:=dist
GO ?= go

.PHONY: all
all: binary-build

#
# Go build related tasks
#
.PHONY: go-install
go-install:
	GO111MODULE=on $(GO) install .

.PHONY: go-run
go-run: go-install
	GO111MODULE=on $(GO) run . 10247 --v=9

.PHONY: go-fmt
go-fmt:
	gofmt -s -e -d $(shell find . -name "*.go" | grep -v /vendor/)

.PHONY: go-vet
go-vet:
	echo $(GO) vet $(shell $(GO) list ./... | grep -v /vendor/)


#
# Docker-related tasks
#
.PHONY: binary-build
binary-build:
	mkdir -p ${DIST_DIR}
	GO111MODULE=on GOOS=linux GOARCH=amd64 go build -o ${DIST_DIR}/${BINARY_NAME}-linux .
	GO111MODULE=on GOOS=darwin GOARCH=amd64 go build -o ${DIST_DIR}/${BINARY_NAME}-darwin .
	shasum -a 256 ${DIST_DIR}/*

#
# Tests-related tasks
#
.PHONY: unit-test
unit-test: go-install
	go test -v ./oke

.PHONY: integ-test
integ-test: go-install

