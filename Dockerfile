# Build Stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build lightweight binary
RUN CGO_ENABLED=0 GOOS=linux go build -o ticketpulse-api ./cmd/api

# Final Stage (Minimal Alpine Runtime)
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/ticketpulse-api .
COPY --from=builder /app/internal/repository/lua ./internal/repository/lua

EXPOSE 8080

CMD ["./ticketpulse-api"]