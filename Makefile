build:
	go build -o dist/cliamp-web ./cmd/cliamp-web
ui:
	cd webui && npm ci && npm run build
test:
	go vet ./... && go test ./...


.PHONY: build ui test
