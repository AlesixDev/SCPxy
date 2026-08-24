BINARY := scpxy
LDFLAGS := -s -w

.PHONY: build test test-unit test-integration vet fmt clean dist

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/scpxy

test:
	go test ./...

test-unit:
	go test ./internal/...

test-integration:
	go test ./test/...

vet:
	go vet ./...

fmt:
	go fmt ./...

clean:
	rm -rf $(BINARY) $(BINARY).exe dist

dist:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 ./cmd/scpxy
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 ./cmd/scpxy
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe ./cmd/scpxy
