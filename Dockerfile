# Multi-stage build for planner.
#
# The Go server embeds the React bundle from internal/web/static via go:embed,
# so that bundle must exist at `go build` time. Rather than depend on a
# developer-built copy on disk (it is a generated artifact, see .gitignore), we
# rebuild it here so the image is fully self-contained and reproducible.

# Stage 1: build the React bundle (esbuild) into internal/web/static.
FROM node:22-slim AS frontend
WORKDIR /src/web/frontend
COPY web/frontend/package.json web/frontend/package-lock.json ./
RUN npm ci
COPY web/frontend/ ./
# build.mjs writes the bundle to ../../internal/web/static (=/src/internal/web/static).
RUN npm run build

# Stage 2: build the Go binary, embedding the freshly built bundle.
# modernc.org/sqlite is pure Go, so CGO is disabled and the result is static.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/internal/web/static/ ./internal/web/static/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /planner .

# Stage 3: minimal runtime. distroless/static carries CA certificates (needed
# for the Firestore TLS connection) and runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /planner /planner

# Cloud Run injects $PORT; `planner serve` binds 0.0.0.0:$PORT (see cli.go).
# Default the container to the Firestore backend; the project must still be
# supplied via PLANNER_FIRESTORE_PROJECT (or --project) at deploy time.
ENV PLANNER_BACKEND=firestore
ENTRYPOINT ["/planner", "serve"]
