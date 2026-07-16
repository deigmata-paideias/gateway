FROM golang:1.26.5-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ai-gateway ./cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mock-provider ./cmd/mock-provider

FROM gcr.io/distroless/static-debian13:nonroot AS gateway
WORKDIR /app
COPY --from=build /out/ai-gateway /app/ai-gateway
ENTRYPOINT ["/app/ai-gateway"]

FROM gcr.io/distroless/static-debian13:nonroot AS mock-provider
WORKDIR /app
COPY --from=build /out/mock-provider /app/mock-provider
ENTRYPOINT ["/app/mock-provider"]
