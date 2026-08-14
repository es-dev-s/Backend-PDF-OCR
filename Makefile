.PHONY: run tidy

run:
	go run ./cmd/api

tidy:
	go mod tidy
