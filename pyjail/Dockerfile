FROM debian:bookworm-slim AS nsjail-base

RUN apt-get update && apt-get install -y --no-install-recommends curl

COPY scripts/ /app/scripts/
COPY db_backups.tar.gz /app/db_backups.tar.gz
RUN chmod +x /app/scripts/install.sh && /app/scripts/install.sh

FROM nsjail-base AS nsjail-server

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    UV_INSTALL_DIR=/usr/local/bin \
    docker_install=true

RUN curl -LsSf https://astral.sh/uv/0.10.9/install.sh | sh

WORKDIR /app

COPY pyproject.toml uv.lock ./

RUN uv sync --frozen

COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

COPY . .

WORKDIR /app/src

EXPOSE 8000

ENTRYPOINT ["/bin/bash", "/app/entrypoint.sh"]
