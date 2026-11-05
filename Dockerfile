# syntax=docker/dockerfile:1
# --- build stage ---
FROM golang:1.24-bookworm AS build
WORKDIR /src

# go-sqlite3 uses cgo; fts5 needs an explicit build tag.
ENV CGO_ENABLED=1

# BuildKit cache mounts keep Go's module/build caches out of image layers.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -tags "sqlite_fts5" -ldflags "-X fedigest/internal/web.Version=$(cat VERSION)" -o /out/fedigest ./cmd/server

# --- runtime stage ---
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/fedigest ./fedigest
COPY themes ./themes

# Data (SQLite DB + session secret) and uploads are separate volumes.
RUN mkdir -p /app/data /app/uploads

EXPOSE 8080
ENV FEDIGEST_DATA_DIR=/app/data
ENV UPLOADS_DIR=/app/uploads

ENTRYPOINT ["./fedigest"]
