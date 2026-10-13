build:
	go build -o dist/cliamp-web ./cmd/cliamp-web
test:
	go vet ./... && go test ./...
	node webui/app_test.js


.PHONY: build test
