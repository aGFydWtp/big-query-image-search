# syntax=docker/dockerfile:1
#
# Multi-stage build for the image-search-api Cloud Run service.
#
# Build context is the repository root. Build with:
#   docker build -f deploy/Dockerfile -t image-search-api .
#
# Stage 1 compiles a static (CGO-disabled) linux/amd64 binary from
# ./cmd/server. Stage 2 is a minimal distroless static base that runs the
# binary as a non-root user. No environment-specific values are baked into the
# image: all runtime configuration is injected via environment variables
# (see deploy/service.yaml and deploy/.env.example).

# ----- Stage 1: build -----
FROM golang:1.26 AS build

WORKDIR /src

# Cache module downloads independently from source changes.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build a static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ----- Stage 2: runtime -----
# Distroless "static" image: no shell, no package manager, minimal attack
# surface. The :nonroot tag runs as a non-root user (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot

# Stateless: no volumes, no writable persistent state.
COPY --from=build /out/server /server

# Cloud Run injects the PORT environment variable; the server reads it (default
# 8080 for local runs). Document the listen port for local/container use.
EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/server"]
