VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  = -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"

.PHONY: build run test integration lint load clean verify-nsjail docker-build docker-run

# Spec requires nsjail built from the submodule pinned to tag 3.4. Assert that
# before any image build so a drifted submodule fails loudly instead of silently
# shipping the wrong version.
verify-nsjail:
	@test -f external/nsjail/Makefile || { echo "external/nsjail missing — run: git submodule update --init"; exit 1; }
	@desc=$$(git -C external/nsjail describe --tags 2>/dev/null); \
	case "$$desc" in \
	  3.4*) echo "nsjail submodule OK: $$desc";; \
	  *) echo "ERROR: external/nsjail must be at tag 3.4 (found: '$$desc')"; exit 1;; \
	esac

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
	    -d '{"language":"py3","source":"print(\"hello\")","tests":[{"stdin":"","expected_stdout":"hello\n"}]}' \
	    http://localhost:8080/run

docker-build: verify-nsjail
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t goboxd:$(VERSION) .

docker-run: docker-build
	docker run --rm --privileged -p 8080:8080 goboxd:$(VERSION)

clean:
	rm -rf bin/
