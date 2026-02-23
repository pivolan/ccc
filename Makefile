.PHONY: build install clean

UNAME := $(shell uname)

build:
	go build -o ccc
	@if [ "$(UNAME)" = "Darwin" ]; then \
		codesign -f -s - ccc 2>/dev/null || true; \
	fi

install: build
	mkdir -p ~/bin
	install -m 755 ccc ~/bin/ccc
	@if [ "$(UNAME)" = "Darwin" ]; then \
		codesign -f -s - ~/bin/ccc 2>/dev/null || true; \
	fi
	@echo "✅ Installed to ~/bin/ccc"

clean:
	rm -f ccc
	rm -rf build/
