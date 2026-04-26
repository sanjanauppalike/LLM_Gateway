FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o llm-gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot

ENV TMPDIR=/tmp
VOLUME ["/tmp"]
WORKDIR /

COPY --from=builder --chown=nonroot:nonroot /app/llm-gateway /llm-gateway

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/llm-gateway"]
