FROM golang:1.26.5-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ai-gateway ./cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mock-provider ./cmd/mock-provider
# 空命名卷会从镜像目录初始化；占位文件确保数据目录及其所有权被复制。
RUN mkdir -p /out/gateway-data && touch /out/gateway-data/.volume-initialized

FROM gcr.io/distroless/static-debian13:nonroot AS gateway
WORKDIR /app
COPY --from=build /out/ai-gateway /app/ai-gateway
COPY --from=build --chown=65532:65532 /out/gateway-data /var/lib/ai-gateway
ENTRYPOINT ["/app/ai-gateway"]

FROM gcr.io/distroless/static-debian13:nonroot AS mock-provider
WORKDIR /app
COPY --from=build /out/mock-provider /app/mock-provider
ENTRYPOINT ["/app/mock-provider"]
