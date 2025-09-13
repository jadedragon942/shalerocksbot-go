# Start from the official Golang image for building
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

RUN apk add gcc musl-dev

# Build the Go binary (static)
RUN CGO_ENABLED=1 GOOS=linux go build -o app .

# Final minimal image
FROM alpine:latest

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /app/app .

# Copy static and templates folders
#COPY static/ ./static/
#COPY templates/ ./templates/

# Expose port (change if needed)
EXPOSE 8000

CMD ["./app"]
