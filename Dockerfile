# Start from the official Golang 1.23 image
FROM golang:1.23-alpine

# Install necessary tools
RUN apk add --no-cache curl bash

# Install kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/ && \
    kubectl version --client

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod, go.sum, and kubeconfig files
COPY go.mod go.sum config ./

# Set up kubeconfig
ENV KUBECONFIG=/app/config

# Download dependencies
RUN go mod download

# Copy the source code
COPY custom-exporter-v2.go .

# Build the application
RUN go build -o exporter custom-exporter-v2.go

# Expose port 8080
EXPOSE 8080

# Run the application
CMD ["./exporter"]
