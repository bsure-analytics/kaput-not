# Multi-stage build for minimal image size

# Build stage
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /workspace

# Copy go module files
COPY go.mod go.sum ./

# Download dependencies (cached layer)
RUN go mod download

# Copy source code
COPY . .

# Get target architecture from Docker buildx
# TARGETARCH is automatically set by Docker buildx for multi-platform builds
ARG TARGETARCH

# Set Go build environment variables
ENV GOOS=linux
ENV GOARCH=${TARGETARCH}
ENV CGO_ENABLED=0

# Build the binary
# -ldflags="-w -s" to strip debug symbols and reduce size
RUN go build \
    -ldflags="-w -s" \
    -o /kaput-not \
    ./cmd/kaput-not

# Runtime stage
FROM gcr.io/distroless/static:nonroot

# Copy CA certificates from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary
COPY --from=builder /kaput-not /kaput-not

# Use non-root user (distroless provides user 65532)
USER 65532:65532

# Run the binary
ENTRYPOINT ["/kaput-not"]
