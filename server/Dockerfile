# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install git for go mod download
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o /ozx-apm-server ./cmd/server

# Final stage
FROM alpine:3.19

WORKDIR /app

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Copy the binary
COPY --from=builder /ozx-apm-server /app/ozx-apm-server

# Copy config if exists
COPY config.yaml* /app/

# Expose ports (SDK ingestion: 8080, Admin: 8081)
EXPOSE 8080 8081

# Run the server
CMD ["/app/ozx-apm-server"]
