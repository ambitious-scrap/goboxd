VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  = -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"

.PHONY: build run test integration lint load clean

build:
	go build $(LDFLAGS) -o bin/goboxd ./cmd/goboxd

run: build
	./bin/goboxd -config configs/languages.yaml

test:
	go test ./...

integration:
	go test ./tests/... -tags integration -v

lint:
	go vet ./...
	@which staticcheck > /dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed, skipping"

load:
	@which hey > /dev/null 2>&1 || (echo "install hey: go install github.com/rakyll/hey@latest" && exit 1)
	hey -n 1000 -c 50 -m POST \
	    -H "Content-Type: application/json" \
	    -d '{"language":"py3","source":"print(\"hello\")","tests":[{"stdin":"","expected_output":"hello\n"}]}' \
	    http://localhost:8080/run

docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t goboxd:$(VERSION) .

docker-run: docker-build
	docker run --rm --privileged -p 8080:8080 goboxd:$(VERSION)

clean:
	rm -rf bin/
