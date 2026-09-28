.PHONY: all check-layout lint test build e2e mini web

all: check-layout

check-layout:
	./scripts/check-layout.sh

lint: check-layout

test: check-layout

build: check-layout

e2e: check-layout

mini: check-layout

web: check-layout
