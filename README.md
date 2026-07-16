# AI Gateway

[![CI](https://github.com/deigmata-paideias/gateway/actions/workflows/ci.yaml/badge.svg)](https://github.com/deigmata-paideias/gateway/actions/workflows/ci.yaml)

AI Gateway 是一个 Schema 驱动的 Go 服务，对外提供 OpenAI 风格的 Chat Completions、Responses 和 Images Generations 接口，并可在 OpenAI 与 DashScope Backend 之间动态切换。图片响应统一归一化为 Base64。

## 核心能力

- OpenAI：使用官方 `openai-go/v3` SDK 接入 Chat、Responses 与 Image。
- DashScope：使用仓库内的 `net/http` Adapter 接入 Chat、Responses 与 Image。
- YAML Schema：以严格解析的 Bootstrap YAML 和 Gateway YAML 描述监听、存储、Backend、模型 Route、审计、限制和 OTel 配置。
- 动态路由：管理 API 使用 Revision 和 `If-Match` 完成 Backend 切换，运行时原子发布新快照。
- 密钥管理：Provider Credential 与 Gateway Token 独立保存，不绑定用户；SQLite 中使用 AES-256-GCM 密文，Token 使用 SHA-256 摘要鉴权。
- Token 审计：按 Gateway Token 保存调用明细和 Usage Token，并提供明细及聚合查询。
- 可观测性：通过 OTLP/HTTP 导出网关 HTTP、Provider、Token 用量、SQLite、审计和配置切换指标及 Trace。
- 可部署性：Docker Compose 配置包含网关健康探针；独立 Overlay 启动无公网、无真实 Provider Key 的 Mock E2E。

## 接口

数据面默认监听 `:8080`，调用时必须携带 `Authorization: Bearer <Gateway Token>`：

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/images/generations`
- `GET /v1/models`
- `GET /v1/token`

管理面默认仅监听 `127.0.0.1:9090`，提供配置 Revision、Backend、Route、Credential、Gateway Token、审计和用量接口。完整契约见 [OpenAPI YAML](api/openapi/gateway.yaml)。

运维面默认监听 `:8081`：

- `GET /livez`
- `GET /readyz`

## 配置

- [Bootstrap 示例](configs/bootstrap.example.yaml)：进程启动所需的监听、SQLite、主密钥文件、初始 Credential 导入、OTel 和健康检查参数。
- [Gateway 示例](configs/gateway.example.yaml)：Backend、模型 Route、审计保留、Responses 绑定和资源限制。

YAML 采用严格字段校验，未知字段、重复 ID、无效能力组合或不存在的引用会阻止配置生效。动态修改后的配置会形成新的 SQLite Revision；Route 切换示例：

```bash
curl -X PUT http://127.0.0.1:9090/admin/v1/routes/chat-default/active-backend \
  -H 'Content-Type: application/json' \
  -H 'If-Match: 1' \
  -d '{"backend_id":"dashscope-cn"}'
```

## Docker Compose 部署

先从 `deploy/secrets/*.example` 创建同名密钥文件并填入真实值，再启动生产 Compose：

```bash
docker compose -f deploy/compose.yaml up --build --detach --wait
```

主密钥文件必须是 Base64 编码的 32 字节随机值。Provider Key 不应写入 YAML、日志或版本库。停止并保留 SQLite 数据：

```bash
docker compose -f deploy/compose.yaml down
```

## 质量门禁

GitHub Actions 是本项目的标准验证环境：

- `go vet ./...` 和 `go build ./cmd/...`。
- `go test -race -count=1 ./...`。
- 手写生产代码语句覆盖率不低于 90%。
- Docker Compose Mock E2E 覆盖两个 Provider 的同步、SSE、Responses 续接、图片 Base64、动态切换、错误注入、SQLite 重启恢复、Token 轮换/吊销、审计和 OTel 信号。

本地需要验证时可运行 `make verify`；Mock E2E 使用 `make e2e`。两者都可能启动或访问本机开发资源，请按自身环境决定是否执行。

## 设计与实施记录

- [详细设计 01](docs/design/design_01.md)
- [详细设计 02](docs/design/design_02.md)
- [实施记录 01](docs/implementation/implementation_01.md)

当前版本采用“无迁移，直接替换”策略，不提供旧 bbolt 数据迁移。SQLite 只支持单个网关实例写入同一文件，不应放在网络文件系统上。
