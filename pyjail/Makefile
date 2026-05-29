.PHONY: *

.DEFAULT_GOAL := run

build:
	@docker compose build web integration-tests

build-no-cache:
	@docker compose build web integration-tests --no-cache

check:
	@docker compose run --rm web bash -c " \
        uv run ruff format --check . && \
        uv run ruff check . \
    "

down:
	@docker compose down -v --remove-orphans

format:
	@docker compose run --rm web uv run ruff format .

generate-proto:
	@docker compose run --rm web bash -c "protoc --python_out=. proto/*.proto"

lint:
	@docker compose run --rm web uv run ruff check --fix .

run:
	@docker compose up

run-integration:
ifdef test
	@docker compose --profile testing run --rm -e TEST_FILTER="$(test)" integration-tests
else
	@docker compose --profile testing run --rm integration-tests
endif

run-tests:
	@docker compose run --rm web uv run python tests/unit/run_unit_tests.py

shell:
	@docker compose run --rm web bash

uv-lock:
	@docker compose run --rm web uv lock
