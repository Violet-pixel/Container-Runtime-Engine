BIN := bin/silo
GOFLAGS_BUILD := -trimpath -ldflags "-s -w"

.PHONY: all build ui test e2e lint rootfs install clean dev

all: ui build

build:
	CGO_ENABLED=1 go build $(GOFLAGS_BUILD) -o $(BIN) ./cmd/silo

ui:
	cd web && npm ci && npm run build

test:
	go test ./...

e2e: build rootfs
	sudo SILO_E2E=1 SILO_E2E_ROOTFS=$(CURDIR)/rootfs.tar.gz go test -count=1 -v ./test/

lint:
	go vet ./...
	cd web && npm run typecheck

rootfs:
	./scripts/mkrootfs.sh rootfs.tar.gz

install:
	./scripts/install.sh

dev:
	cd web && npm run dev

clean:
	rm -rf bin rootfs.tar.gz
