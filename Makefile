build:
	go build -o dist/spindle ./cmd/spindle
# release builds: one static binary per platform in dist/release/
release:
	@mkdir -p dist/release
	@for t in linux/amd64 linux/arm64 linux/arm darwin/arm64 darwin/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  GOOS=$$os GOARCH=$$arch GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/release/spindle-$$os-$$arch ./cmd/spindle || exit 1; \
	done; ls -la dist/release

test:
	go vet ./... && go test ./...
	node webui/app_test.js


.PHONY: build release test
