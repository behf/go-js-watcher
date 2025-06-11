# Stage 1: Build the Go application
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker cache
# This means go mod download only runs if dependencies change
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go application
# CGO_ENABLED=0 is important for static binaries, especially for Alpine base images
# GOOS=linux ensures it's built for Linux inside the container
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# Stage 2: Create the final lean image
FROM alpine:latest

WORKDIR /app

# Copy the built executable from the builder stage
COPY --from=builder /app/main .

# Copy necessary static assets and templates
# These paths are relative to the WORKDIR in the builder stage
COPY --from=builder /app/static ./static
COPY --from=builder /app/templates ./templates

# Expose the port your application listens on
EXPOSE 8090

# Command to run the application when the container starts
# The .env file is NOT copied into the image for security.
# Environment variables will be passed at runtime (see `docker run` command below).
CMD ["./main"]