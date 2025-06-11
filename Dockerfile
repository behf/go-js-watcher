# Stage 1: Build the Go application
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker cache
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Install dependencies for cgo and sqlite3
RUN apk add --no-cache gcc musl-dev sqlite-dev

# Build the Go application with CGO enabled
ENV CGO_ENABLED=1
RUN GOOS=linux go build -o main .

# Stage 2: Create the final lean image
FROM alpine:latest

WORKDIR /app

# Install the necessary SQLite C library for go-sqlite3
RUN apk add --no-cache sqlite-libs

# Copy the built executable from the builder stage
COPY --from=builder /app/main .

# Copy static assets and templates
COPY --from=builder /app/static ./static
COPY --from=builder /app/templates ./templates

# Expose the port your application listens on
EXPOSE 8090

# Command to run the application when the container starts
CMD ["./main"]
