BIN    := virt-applet
PREFIX ?= $(HOME)/.local

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BIN) .

install: build
	install -Dm755 $(BIN) $(PREFIX)/bin/$(BIN)
	install -Dm644 virt-applet.service $(HOME)/.config/systemd/user/virt-applet.service
	systemctl --user daemon-reload
	@echo "Enable at login with: systemctl --user enable --now virt-applet"

uninstall:
	-systemctl --user disable --now virt-applet
	rm -f $(PREFIX)/bin/$(BIN) $(HOME)/.config/systemd/user/virt-applet.service

deb:
	./build-deb.sh $(VERSION)

.PHONY: build install uninstall deb
