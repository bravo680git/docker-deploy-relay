# Stage 1: Build the Go binary
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum files
COPY go.mod ./
# RUN go mod download

# Copy the source code
COPY . .

# Build the application
RUN go build -o docker-deploy-relay .

# Stage 2: Final image
FROM alpine:3.20

# Install Docker CLI and Docker Compose
RUN apk add --no-cache docker-cli docker-cli-compose

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/docker-deploy-relay /app/docker-deploy-relay

# The webhook server listens on PORT env var (defaults to 8080 if not set)
EXPOSE 8080

# Run the application
CMD ["/app/docker-deploy-relay"]
