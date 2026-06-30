# Multi-stage build for the CRN Go backend.
#
# Stage 1 (builder): compile a static binary against the pinned Go toolchain.
# Stage 2 (runtime): a distroless base for a small, non-root image.
#
# NOTE: the running container must have the `claude` CLI and a docker client
# available to actually spawn builds (architecture §2.4). This image ships only
# the API/WS server; mount the docker socket and provide CRN_CLAUDE_BIN in the
# deployment. (TODO(devops): bake claude + docker CLI into the runtime stage, or
# run the backend with access to the host toolchain.)

# ---- build stage ----
FROM golang:1.23-alpine AS builder
WORKDIR /src

# Cache module downloads separately from source for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO disabled => fully static binary that runs on distroless/static.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/crn-server ./cmd/server

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

# Ship migrations alongside the binary so `make migrate` equivalents / entrypoint
# tooling can find them inside the image.
COPY --from=builder /out/crn-server /app/crn-server
COPY --from=builder /src/migrations /app/migrations

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/crn-server"]
