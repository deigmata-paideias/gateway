# AI 网关实施记录 01

- 实施版本：0.1.0
- 对应设计：[AI 网关详细设计 02](../design/design_02.md)
- 实施日期：2026-07-16
- 最后验证日期：2026-07-16
- 状态：待 GitHub Actions 验收
- 目标仓库：<https://github.com/deigmata-paideias/gateway>

## 1. 前置说明

### 1.1 输入与假设

- 需求输入来自 [Input 01](../chat/input_01.md) 至 [Input 06](../chat/input_06.md)，以用户最新指令为准。
- 两个 Provider 分别为 OpenAI 和 DashScope；OpenAI 使用官方 Go SDK，DashScope 使用自建 HTTP Adapter。
- 数据面以 Gateway Token 鉴权和审计，但不建设用户、角色、租户或权限系统。
- SQLite 只由单实例写入；Provider Key、Gateway Token 明文、提示词、模型正文和图片内容不写入日志或审计。
- 用户于 2026-07-16 明确要求将项目上传到 GitHub 并由 GitHub Actions 执行验证，不使用本机运行资源。

### 1.2 指令偏差与回滚

全局指南默认禁用远端 CI/CD，但用户明确指定 GitHub 仓库并要求使用 GitHub CI，因此本次新增 `.github/workflows/ci.yaml`。影响范围仅为 GitHub Actions 的构建、测试、覆盖率和 Mock E2E，不包含发布或部署。回滚时删除该工作流；业务代码和本地 Make 目标不受影响。

本会话没有适用于 Markdown、YAML 和 Go 源码的专用编辑 MCP，因此使用 `apply_patch` 在当前工作区内创建和修改文件。模块路径统一属于跨文件机械替换，使用安全 Shell 完成；其回滚方式是将 `github.com/deigmata-paideias/gateway` 反向替换为原模块路径。所有运行验证均交给 GitHub Actions，本机不再启动 Docker 或服务。

## 2. 实施范围

### 2.1 数据面

- `POST /v1/chat/completions`：OpenAI、DashScope，同步和 SSE。
- `POST /v1/responses`：OpenAI、DashScope，同步和 SSE；公共 Response ID 绑定原 Gateway Token、模型和 Backend。
- `POST /v1/images/generations`：两个 Provider 的结果统一为 `data[].b64_json`。
- `GET /v1/models`：查询当前 Gateway Token 可调用的模型别名。
- `GET /v1/token`：查询当前 Gateway Token 元数据。

### 2.2 管理面

- 严格 YAML 初始化配置、SQLite Revision、CAS 更新和历史 Revision 恢复。
- Backend 增删改查和 Route Active Backend 动态切换。
- Provider Credential 创建、轮换、查询和删除。
- Gateway Token 创建、密文取回、轮换、吊销、查询和删除。
- Token 粒度调用审计、筛选和 Usage Token 聚合。

### 2.3 运行能力

- SQLite WAL、外键、Busy Timeout、事务迁移和重启恢复。
- AES-256-GCM 静态加密、Gateway Token SHA-256 摘要鉴权。
- OpenTelemetry OTLP/HTTP Metrics 与 Traces；导出失败不阻断业务。
- 独立数据面、管理面和运维面监听，以及 `/livez`、`/readyz`。
- 多阶段 Dockerfile、生产 Compose、健康探针、OTel Collector 和 Mock E2E Overlay。

## 3. 质量门禁

GitHub Actions 工作流按以下顺序执行：

1. 使用 `actions/checkout@v6` 和 `actions/setup-go@v6` 检出代码并安装 `go.mod` 锁定的 Go 版本。
2. 检查 `gofmt` 差异，执行 `go vet ./...` 和 `go build ./cmd/...`。
3. 执行 `go test -race -count=1 ./...`。
4. 生成 `coverage.out`，通过仓库内工具强制语句覆盖率不低于 90%。
5. 在独立 Job 中校验 Compose 配置并执行 Mock E2E；保存覆盖率和 Compose 日志制品 14 天。

Mock E2E 不使用真实 Provider Key，也不访问 Provider 公网。它覆盖两个 Provider 的 Chat、Responses、Image、同步、SSE、动态 Backend 切换、Responses Backend 固定、跨 Token 拒绝、图片 Base64、429、损坏 SSE、Token 审计、用量聚合、SQLite 重启恢复、Token 轮换/吊销及 OTel 导出证据。

## 4. 验证证据

| 项目 | 结果 | 证据位置 |
| --- | --- | --- |
| GitHub Actions 构建与静态检查 | 待执行 | `.github/workflows/ci.yaml` 的 `quality` Job |
| 单元测试与竞态检查 | 待执行 | `quality` Job 日志 |
| 语句覆盖率不低于 90% | 待执行 | `unit-test-coverage` Artifact 与 Job 日志 |
| Docker Compose Mock E2E | 待执行 | `mock-e2e` Job 与 `mock-e2e-compose-logs` Artifact |
| 运行时健康探针 | 待执行 | E2E 的 `docker compose up --wait` 日志 |

CI 完成后应将工作流 URL、提交 SHA、覆盖率和 E2E 结果补充到本节，再将状态改为“已验收”。

## 5. 迁移与回滚

本版本没有已部署的旧数据，采用“无迁移，直接替换”。运行时回滚应保留 SQLite 卷和部署前数据库备份，切回上一容器镜像及其匹配配置；若回滚版本不兼容当前 Schema，应使用对应版本的数据库备份，不应直接降级读取。

GitHub Actions 回滚只需删除 `.github/workflows/ci.yaml`。远端首次提交如需整体撤销，应使用新增的反向提交，保留 Git 审计历史。
