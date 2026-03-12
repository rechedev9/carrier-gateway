# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Download dependencies first for better layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a statically linked binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/carrier-gateway ./cmd/carrier-gateway

# ---- runtime stage ----
# distroless/static includes TLS root certs (needed for HTTPS carrier calls)
# and has near-zero attack surface like scratch.
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /bin/carrier-gateway /bin/carrier-gateway

# Expose the default HTTP port.
EXPOSE 8080

ENTRYPOINT ["/bin/carrier-gateway"]
