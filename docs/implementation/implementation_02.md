# AI 网关实施记录 02

- 实施版本：0.1.0 Quickstart 与真实 Provider 兼容性迭代
- 对应设计：[AI 网关详细设计 02](../design/design_02.md)
- 输入依据：[Input 07](../chat/input_07.md)
- 实施日期：2026-07-18
- 最后验证日期：2026-07-18
- 状态：本地验收通过，等待本次提交的 GitHub Actions 持续验收
- 目标仓库：<https://github.com/deigmata-paideias/gateway>

## 1. 前置说明

### 1.1 输入、时效与假设

- 用户提供 OpenAI 兼容 Endpoint、OpenAI API Key 和 DashScope API Key，并要求 Example 通过真实调用验证。
- API Key 只写入工作区外的临时 `0600` 文件，并通过运行期 Docker Secret 注入；本文、Chat 归档、命令行、Git、镜像层和日志均不保存明文。
- Provider 模型清单和接口行为于 2026-07-18 实测，结果受账号授权和上游版本影响。OpenAI 兼容 Endpoint 当前只开放文本模型，且 `/v1/responses` 返回 `not implemented`；Quickstart 因而只声明其 Chat 能力。核心 OpenAI Provider 仍使用官方 Go SDK 实现 Chat、Responses 和 Image，并由单元测试及 Mock E2E 覆盖。
- DashScope 当前账号实测可调用 `qwen-plus`、`qwen-image-2.0`；Quickstart 分别用于文本和图片能力。

### 1.2 指令偏差与回滚

用户早期要求不使用本机资源，后续明确要求构建镜像、验证 Example，并最终授权自动模式。因此本轮仅为 Quickstart 和 Mock E2E 使用本机 Go、Docker、回环端口及专用命名卷，不访问或修改其他本机项目。回滚方式为停止 `examples/quickstart/compose.yaml`、删除其专用卷和工作区外临时密钥文件；镜像可按 Tag 单独删除。

当前会话未提供 Sequential Thinking、Context7 或 Fetch MCP。实施按指南降级为 `update_plan`、本地检索、`apply_patch`、安全 Shell 和公开官网检索；外部资料仅用于核对 DashScope 当前图片 API，未向外部上传仓库内容或敏感信息。

## 2. 实施变更

### 2.1 Quickstart

- 新增独立 Compose、Bootstrap YAML、Gateway YAML、Docker Secret 示例、健康探针和 Go 客户端。
- 客户端自动创建 Gateway Token，通过 Revision 与 `If-Match` 动态切换 Backend，查询 `/v1/models`，再调用 Chat、Responses 或 Image。
- Image 响应只输出解码字节数与 SHA-256，不打印完整 Base64，也不在本地保存图片。
- `.dockerignore` 排除 Git 元数据、构建输出、数据库和 Secret 目录，避免密钥进入 `COPY . .` 构建上下文。

### 2.2 OpenAI 兼容性

- 继续使用 `github.com/openai/openai-go/v3` 官方 SDK。
- 用户提供的兼容 Endpoint 会拒绝 SDK 默认 `User-Agent`，但接受网关标识；SDK Client 因此显式发送 `User-Agent: ai-gateway/0.1.0`。测试会断言该 Header，API Key 仍由 SDK 的标准 Bearer 鉴权注入。
- Quickstart 的 OpenAI Backend 只声明 `chat`。标准 OpenAI Responses 支持未被删除，只是不把当前不实现 `/v1/responses` 的兼容 Endpoint 错标为可用 Backend。

### 2.3 DashScope 图片原生适配

- 文本能力继续使用 `/compatible-mode/v1` 的 OpenAI 兼容 Chat 与 Responses 接口。
- 图片能力改用 DashScope 原生同步接口 `/api/v1/services/aigc/multimodal-generation/generation`，从兼容 Base URL 的同一 Host 确定性派生原生 URL。
- 适配器把公共 Images 请求的 `prompt`、`n`、`size` 及受支持扩展字段转换为 `input.messages` 与 `parameters`；`1024x1024` 形式会转换为 DashScope 的 `1024*1024`。
- 图片请求强制 `Accept: application/json`。Chat 与 Responses 仍允许 `application/json, text/event-stream`；这避免图片接口选择非 JSON 表示。
- 适配器提取 `output.choices[].message.content[].image` 临时 URL，执行受限下载、大小和媒体类型校验，再只向调用方返回 `data[].b64_json`。
- Mock Provider 和 E2E 同步采用 DashScope 原生请求/响应形态，防止契约测试继续模拟不存在的兼容 Images 路径。

### 2.4 安全失败日志

Provider 调用失败新增结构化日志，只记录 Provider、Backend、操作、公共错误码和安全分类原因。请求正文、Gateway Token、Provider Key、图片 URL、上游响应正文及未知 Transport 错误详情均不记录。

## 3. 故障与根因

| 现象 | 根因证据 | 修复 |
| --- | --- | --- |
| OpenAI SDK Chat 返回 403，等价 curl 返回 200 | 单变量请求确认兼容 Endpoint 拒绝 SDK 默认 `User-Agent` | 官方 SDK Client 设置网关 `User-Agent` |
| OpenAI Responses 经网关返回 502 | 直连 `/v1/responses` 返回 500 和 `not implemented` | Quickstart 不声明该 Endpoint 的 Responses 能力；核心标准能力保留 |
| DashScope `/compatible-mode/v1/images/generations` 返回空 404 | 直连复现；官方文档声明图片使用独立原生接口 | 自建 Adapter 转换并调用原生同步接口 |
| 原生图片接口返回后被判定为非 JSON | 安全失败日志显示首字符为 `i`；与仅声明 JSON 的直连请求对比 | Image 单独设置 `Accept: application/json` |
| 沙箱内 `httptest` 或回环请求失败 | 错误为 `bind/connect: operation not permitted`，授权后同一测试通过 | 仅对本机回环测试降级使用授权执行，不改业务逻辑 |

## 4. 验证证据

### 4.1 真实 Provider 与镜像

| 验证项 | 结果 |
| --- | --- |
| Compose 全新启动健康探针 | 通过，60 秒超时内变为 `healthy` |
| OpenAI Chat | 通过，`qwen3.6-flash` 返回 `gateway-ok` |
| DashScope Chat | 通过，`qwen-plus` 返回 `gateway-ok` |
| DashScope Responses | 通过，返回标准 `response` 对象和 `response-ok` |
| DashScope Image | 通过，`qwen-image-2.0` 返回 1 张 Base64 图片；解码后 204,016 字节，SHA-256 为 `f9da664b0639309ad2d319891035a0e5c0940c5a3e8f4b3cf2f7c25c7f2a7d1e` |
| Token 粒度审计 | 四类最终调用均为 `succeeded`、HTTP 200；图片记录 `image_count=1`、`raw_image_bytes=204016` |

### 4.2 本地质量门

| 命令 | 结果 |
| --- | --- |
| `make verify` | Build、普通测试、Race、Vet、生产/E2E/Quickstart Compose 校验全部通过 |
| 覆盖率门禁 | 总语句覆盖率 93.5%，最低包 `internal/provider/dashscope` 为 90.7%，均不低于 90% |
| `make e2e` | Docker Compose Mock E2E 通过，用时 198.270 秒 |

已通过的 Quickstart 基线 CI：<https://github.com/deigmata-paideias/gateway/actions/runs/29634381226>。本轮最终提交仍需由同一工作流复验；最终 Run URL 在交付回复中给出。

## 5. 外部证据

检索日期：2026-07-18。关键词：`qwen-image-2.0 API DashScope image generation HTTP endpoint`、`百炼 qwen-image-2.0 图像生成 API curl`。筛选条件：仅采用阿里云官方帮助中心。

- [千问 Qwen-Image 文生图 API 调用方法](https://help.aliyun.com/zh/model-studio/qwen-image-api)：采用同步接口 URL、`input.messages`、`parameters`、图片 URL 响应形态及 URL 24 小时有效期说明。

## 6. 迁移与回滚

本轮没有数据库 Schema 迁移，采用“无迁移，直接替换”。回滚代码时可切回上一镜像；Gateway YAML 已持久化为 SQLite Revision，回滚前应保留专用卷或数据库备份。若回滚到旧 DashScope Adapter，图片真实调用会重新落到不存在的兼容路径，因此只建议回滚整个 Quickstart 变更，不建议仅回滚请求转换。
