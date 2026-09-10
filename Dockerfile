FROM golang:1.26-alpine AS builder

WORKDIR /src

# Контракты и общий обвяз — отдельные модули, приезжают из сети по версии.
# Слой с зависимостями отделён от кода: он меняется только при повышении
# версии в go.mod и переиспользуется между сборками.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/inventory ./cmd/inventory

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/inventory /inventory
EXPOSE 8004
USER nonroot:nonroot
ENTRYPOINT ["/inventory"]
