build:
	go build -o dist/cliamp-web ./cmd/cliamp-web
ui:
	cd webui && npm ci && npm run build
test:
	go vet ./... && go test ./...
	node webui2/app_test.js


.PHONY: build ui test
