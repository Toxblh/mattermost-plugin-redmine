PLUGIN_ID := space.altlinux.redmine
VERSION := 0.1.0
GO ?= go

.PHONY: build test bundle clean
build:
	cd webapp && npm ci && npm run typecheck && npm run build
	mkdir -p server/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags='-s -w' -o server/dist/plugin-linux-amd64 ./server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags='-s -w' -o server/dist/plugin-linux-arm64 ./server

test:
	$(GO) test -race ./server
	cd webapp && npm ci && npm run typecheck && npm test

bundle: build
	mkdir -p dist/$(PLUGIN_ID)
	cp plugin.json dist/$(PLUGIN_ID)/
	mkdir -p dist/$(PLUGIN_ID)/server dist/$(PLUGIN_ID)/webapp
	cp -R server/dist dist/$(PLUGIN_ID)/server/
	cp -R webapp/dist dist/$(PLUGIN_ID)/webapp/
	cd dist && tar -czf $(PLUGIN_ID)-$(VERSION).tar.gz $(PLUGIN_ID)

clean:
	rm -rf dist server/dist webapp/dist
