# Quickstart Example

本 Example 使用一个 Gateway 容器连接 OpenAI 兼容 Endpoint 与 DashScope，演示 YAML 配置、Docker Secret、Gateway Token、动态 Backend 切换、Chat、Responses 和 Image Base64。

## 1. 准备 Secret

完整 API Key 不应写入 YAML、Git、Shell 历史或镜像层。先创建被 `.gitignore` 排除的本地 Secret 文件：

```bash
mkdir -p examples/quickstart/secrets
openssl rand -base64 32 > examples/quickstart/secrets/ai_gateway_master_key
chmod 600 examples/quickstart/secrets/ai_gateway_master_key
```

再通过可信编辑器分别写入以下文件，文件只包含 Key 本身且末尾可带换行：

- `examples/quickstart/secrets/openai_api_key`
- `examples/quickstart/secrets/dashscope_api_key`

默认 OpenAI Base URL 是 `https://ai.112102.xyz/v1`，DashScope Base URL 是 `https://dashscope.aliyuncs.com/compatible-mode/v1`。模型映射位于 [gateway.yaml](gateway.yaml)，可按账号实际可用模型调整。

2026-07-18 的实际 `/models` 查询显示，该 OpenAI 兼容 Endpoint 的当前凭证只开放文本模型；直接调用 `/v1/responses` 还会返回 `not implemented`。因此 Quickstart 只使用 `qwen3.6-flash` 验证 OpenAI Chat，不把该 Backend 加入 Responses 或 Image Route。核心网关仍通过 OpenAI 官方 Go SDK 支持标准 OpenAI Responses API，并由契约测试覆盖。DashScope 使用 `qwen-plus` 和 `qwen-image-2.0` 验证 Chat、Responses 与 Image。此结果具有账号和时间范围，模型授权变化后应重新查询并更新 YAML。

## 2. 构建并启动

```bash
docker compose -f examples/quickstart/compose.yaml config --quiet
docker compose -f examples/quickstart/compose.yaml up --build --detach --wait
```

容器健康后，数据面、管理面和运维面分别监听：

- `http://127.0.0.1:18080`
- `http://127.0.0.1:19090`
- `http://127.0.0.1:18081`

## 3. 运行客户端

默认通过 OpenAI Backend 执行一次 Chat：

```bash
go run ./examples/quickstart -provider openai -operation chat
```

动态切换到 DashScope 后执行 Chat：

```bash
go run ./examples/quickstart -provider dashscope -operation chat
```

Responses 和 Image 必须显式选择 DashScope：

```bash
go run ./examples/quickstart -provider dashscope -operation responses
go run ./examples/quickstart -provider dashscope -operation image -prompt '一只像素风格的蓝色小鸟'
```

`-operation image` 会产生 Provider 费用。客户端不会打印完整 Base64，只输出解码后的图片字节数和 SHA-256。当前 `-operation all` 只适用于 DashScope；客户端会在调用前拒绝该 OpenAI 兼容 Endpoint 不支持的 Responses 与 Image 组合。

每次运行客户端都会创建一个独立 Gateway Token，切换 Route 时使用当前 Revision 和 `If-Match`，因此同时演示了 Token 审计和动态 Backend API。

## 4. 停止与重置

停止服务但保留 SQLite 数据：

```bash
docker compose -f examples/quickstart/compose.yaml down
```

需要重新导入 Provider Key 或重置配置 Revision 时，显式删除 Quickstart 卷：

```bash
docker compose -f examples/quickstart/compose.yaml down --volumes
```

删除卷会永久清除本 Example 的 SQLite 数据；不会影响其他 Compose Project。
