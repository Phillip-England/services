PREFIX ?= /usr/local
DESTDIR ?=

.PHONY: run build install test

run:
	bun run src/index.tsx

build:
	bun install --frozen-lockfile
	bun run build

install: build
	sudo install -d "$(DESTDIR)$(PREFIX)/bin"
	sudo install -m 0755 dist/services "$(DESTDIR)$(PREFIX)/bin/services"

test:
	bun run typecheck
	bun test
