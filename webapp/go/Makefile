all: build
GO := GOPATH=`pwd` go

.PHONY: init
init: 
	$(GO) get -d ./...

.PHONY: build
build: 
	$(GO) install torb
