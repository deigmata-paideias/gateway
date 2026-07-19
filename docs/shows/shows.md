# 效果演示

## 1. 配置并启动

### 1.2 配置 secret

提供了三种 compose 模式，演示这里用 quickstart 模式。配置 dashscope openai gw key 之后启动：

`docker compose -f examples/quickstart/compose.yaml up --build --detach --wait`

容器启动：

```shell
[+] up 2/2                                                                                                                                            
 ✔ Image ai-gateway:quickstart               Built                                                                                                3.2s
 ✔ Container ai-gateway-quickstart-gateway-1 Healthy                                                                                              6.2s

$ docker ps | grep "gateway"
fcc8182e0135   ai-gateway:quickstart   "/app/ai-gateway ser…"   About a minute ago   Up About a minute (healthy)   127.0.0.1:18080->8080/tcp, 127.0.0.1:18081->8081/tcp, 127.0.0.1:19090->9090/tcp   ai-gateway-quickstart-gateway-1
```

## 2. 请求示例

使用 curl 直接请求，或者用 examples/quickstart/main.go。

分为 CP 和 DP 面 API。

### 2.1 调用演示

#### 2.1.1 获取 gw token

```shell
# 生成 token
$ curl -s -X POST http://127.0.0.1:19090/admin/v1/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name":"dev"}'
  
{"id":"gtok_06fqms74111vcpsyevy316e5c8","name":"dev","token":"agw_2Q2IFMTN2WFLL7AHYWOYUZJ6QV","status":"active","created_at":1784464860168,"updated_at":1784464860168,"expires_at":null}
  
# 获取 token
$ curl -s http://127.0.0.1:19090/admin/v1/tokens/gtok_06fqms74111vcpsyevy316e5c8/secret

{"id":"gtok_06fqms74111vcpsyevy316e5c8","token":"agw_2Q2IFMTN2WFLL7AHYWOYUZJ6QV"}
```

#### 2.1.2 获取当前网关配置

```shell
$ curl -s http://127.0.0.1:19090/admin/v1/config | jq

{
  "config": {
    "api_version": "gateway.ai/v1alpha2",
    "kind": "GatewayConfig",
    "backends": [
      {
        "id": "openai-quickstart",
        "provider": "openai",
        "base_url": "https://ai.112102.xyz/v1",
        "credential_id": "openai-quickstart",
        "capabilities": [
          "chat"
        ],
        "timeouts": {
          "request": "3m0s",
          "stream_idle": "1m0s"
        }
      },
      {
        "id": "dashscope-quickstart",
        "provider": "dashscope",
        "base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
        "credential_id": "dashscope-quickstart",
        "capabilities": [
          "chat",
          "responses",
          "image"
        ],
        "timeouts": {
          "request": "3m0s",
          "stream_idle": "1m0s"
        }
      }
    ],
    "routes": [
      {
        "id": "chat-default",
        "operation": "chat",
        "model_alias": "chat-default",
        "active_backend": "openai-quickstart",
        "targets": [
          {
            "backend_id": "openai-quickstart",
            "upstream_model": "qwen3.6-flash"
          },
          {
            "backend_id": "dashscope-quickstart",
            "upstream_model": "qwen-plus"
          }
        ]
      },
      {
        "id": "responses-default",
        "operation": "responses",
        "model_alias": "responses-default",
        "active_backend": "dashscope-quickstart",
        "targets": [
          {
            "backend_id": "dashscope-quickstart",
            "upstream_model": "qwen-plus"
          }
        ]
      },
      {
        "id": "image-default",
        "operation": "image",
        "model_alias": "image-default",
        "active_backend": "dashscope-quickstart",
        "targets": [
          {
            "backend_id": "dashscope-quickstart",
            "upstream_model": "qwen-image-2.0"
          }
        ]
      }
    ],
    "audit": {
      "retention": "168h0m0s",
      "cleanup_interval": "1h0m0s",
      "abandoned_after": "15m0s",
      "cleanup_batch_size": 1000
    },
    "responses": {
      "binding_retention": "168h0m0s"
    },
    "limits": {
      "request_body_bytes": 2097152,
      "max_backends": 10,
      "max_routes": 10,
      "chat_concurrency": 16,
      "responses_concurrency": 16,
      "image_concurrency": 2,
      "images_per_request": 1,
      "image_raw_bytes_per_response": 33554432
    }
  },
  "revision": 1
}

# schema 配置版本查看：
curl -s http://127.0.0.1:19090/admin/v1/config | jq '.revision'

1
```

#### 2.1.3 获取可用的所有 bakcend

```shell
$ curl -s http://127.0.0.1:19090/admin/v1/backends | jq
{
  "data": [
    {
      "id": "openai-quickstart",
      "provider": "openai",
      "base_url": "https://ai.112102.xyz/v1",
      "credential_id": "openai-quickstart",
      "capabilities": [
        "chat"
      ],
      "timeouts": {
        "request": "3m0s",
        "stream_idle": "1m0s"
      }
    },
    {
      "id": "dashscope-quickstart",
      "provider": "dashscope",
      "base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
      "credential_id": "dashscope-quickstart",
      "capabilities": [
        "chat",
        "responses",
        "image"
      ],
      "timeouts": {
        "request": "3m0s",
        "stream_idle": "1m0s"
      }
    }
  ],
  "revision": 1
}
```

#### 2.1.4 调用 openai chat 和 image 接口查看 response

```shell
# token：gtok_06fqms74111vcpsyevy316e5c8

# simple chat
curl -s http://127.0.0.1:18080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer agw_2Q2IFMTN2WFLL7AHYWOYUZJ6QV" \
  -d '{"model":"chat-default","messages":[{"role":"user","content":"用一句话介绍自己"}]}' | jq

{
  "choices": [
    {
      "finish_reason": "stop",
      "index": 0,
      "logprobs": null,
      "message": {
        "content": "我是通义千问，由阿里巴巴通义实验室研发的大语言模型，始终专注为你提供准确、高效且贴心的智能帮助。",
        "reasoning_content": "Here's a thinking process:\n\n1.  **Analyze User Input:**\n   - **Input:** \"用一句话介绍自己\" (Introduce yourself in one sentence)\n   - **Language:** Chinese\n   - **Requirement:** One sentence, self-introduction\n\n2.  **Identify Key Constraints:**\n   - Must be exactly one sentence (in Chinese, typically ending with a period)\n   - Must introduce myself (the AI assistant)\n   - Should be concise, clear, and accurate\n\n3.  **Determine Core Identity/Attributes:**\n   - I am Qwen (通义千问), developed by Alibaba Group's Tongyi Lab.\n   - I'm an AI assistant designed to help with answering questions, creating content, coding, logical reasoning, etc.\n   - Key traits: helpful, capable, thoughtful, always here to assist.\n\n4.  **Draft - Mental Refinement (Chinese):**\n   - 我是通义千问，由阿里巴巴通义实验室研发的大语言模型，致力于为你提供清晰、准确且有用的 帮助。\n   - Check length: One sentence? Yes.\n   - Covers identity + purpose? Yes.\n   - Natural & professional? Yes.\n   - Matches official identity? Yes (Qwen / 通义千问, Alibaba).\n\n5.  **Final Polish (One Sentence Requirement):**\n   Ensure it strictly meets \"一句话\" (one sentence). The draft is already one sentence with a comma and ends with a period. It's concise and accurate.\n\n   Alternative: 我是通义千问，一款专注于为你提供高效、可 靠智能辅助的AI助手。 (Slightly more user-focused, but the first version is better aligned with official positioning.)\n\n   Let's stick with: 我是通义千问，由阿里巴巴通义实验室研发的AI大模型，始终致力于为你提供清晰、准确且实用的帮助。\n\n   Wait, the prompt says \"用一句话介绍自己\". I'll keep it tight:\n   “我是通义千问，由阿里巴巴通义实验室研发的大语言模型，始终专注为你提供准确、高效且有温度的智能帮助。”\n\n   Check constraints: One sentence? Yes. Accurate? Yes. Natural? Yes.\n\n6.  **Output Generation:** (Matches the refined version)\n   “我是通义千问，由阿里巴巴通义实验室研发的大语言模型，始终专注为你提供准确、高效且贴心的智能帮助。”✅\n   (Note: I'll keep it precise and within standard brand guidelines while staying conversational.) \n   Final check against instructions: \"用一句话介绍自己\" -> strictly one sentence. Delivered.✅\n   Proceed. \n   Output matches the response. \n   *(Self-Correction/Verification during thought)*\n   - Official name: 通义千问 / Qwen\n   - Developer: 阿里巴巴集团旗下通义实验室\n   - Role: 大语言模型 / AI 助手\n   - One sentence constraint: Met.\n   - Tone: Professional yet approachable.\n   All good.✅\n   Output generation. \n   “我是通义千问，由阿里巴巴通义实验室研发的大语言模型，始终致力于为你提供准确、高效且有温度的智能帮助。” (Slight tweak for natural flow) -> Perfect. \n   Proceeds. \n   [Output matches the final refined version]\n   *(Done.)*",                                                                                              
        "role": "assistant"
      }
    }
  ],
  "created": 1784465491,
  "id": "chatcmpl-9edfc1eb-8a1d-9b96-9f9d-156045770e98",
  "model": "chat-default",
  "object": "chat.completion",
  "system_fingerprint": null,
  "usage": {
    "completion_tokens": 734,
    "completion_tokens_details": {
      "reasoning_tokens": 702,
      "text_tokens": 734
    },
    "prompt_tokens": 14,
    "prompt_tokens_details": {
      "text_tokens": 14
    },
    "total_tokens": 748
  }
}

# stream chat
curl -sN http://127.0.0.1:18080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer agw_2Q2IFMTN2WFLL7AHYWOYUZJ6QV" \
  -d '{"model":"chat-default","stream":true,"messages":[{"role":"user","content":"讲个短笑话"}]}'

data: {"choices":[{"delta":{"content":"突然看到一块五分"},"finish_reason":null,"index":0,"logprobs":null}],"created":1784465579,"id":"msg_7537d5ff-c943-47f6-9d8b-fc9f33220880","model":"chat-default","object":"chat.completion.chunk","system_fingerprint":null,"usage":null}

data: {"choices":[{"delta":{"content":"熟的牛排，却没有"},"finish_reason":null,"index":0,"logprobs":null}],"created":1784465579,"id":"msg_7537d5ff-c943-47f6-9d8b-fc9f33220880","model":"chat-default","object":"chat.completion.chunk","system_fingerprint":null,"usage":null}

data: {"choices":[{"delta":{"content":"打招呼。  \n为什么"},"finish_reason":null,"index":0,"logprobs":null}],"created":1784465579,"id":"msg_7537d5ff-c943-47f6-9d8b-fc9f33220880","model":"chat-default","object":"chat.completion.chunk","system_fingerprint":null,"usage":null}

data: {"choices":[{"delta":{"content":"？  \n因为它们**"},"finish_reason":null,"index":0,"logprobs":null}],"created":1784465579,"id":"msg_7537d5ff-c943-47f6-9d8b-fc9f33220880","model":"chat-default","object":"chat.completion.chunk","system_fingerprint":null,"usage":null}

data: {"choices":[{"delta":{"content":"不熟**。"},"finish_reason":null,"index":0,"logprobs":null}],"created":1784465579,"id":"msg_7537d5ff-c943-47f6-9d8b-fc9f33220880","model":"chat-default","object":"chat.completion.chunk","system_fingerprint":null,"usage":null}

data: {"choices":[{"delta":{"content":"😄"},"finish_reason":null,"index":0,"logprobs":null}],"created":1784465579,"id":"msg_7537d5ff-c943-47f6-9d8b-fc9f33220880","model":"chat-default","object":"chat.completion.chunk","system_fingerprint":null,"usage":null}

data: {"choices":[{"delta":{},"finish_reason":"stop","index":0,"logprobs":null}],"created":1784465579,"id":"msg_7537d5ff-c943-47f6-9d8b-fc9f33220880","model":"chat-default","object":"chat.completion.chunk","system_fingerprint":null,"usage":null}

data: {"choices":[],"created":1784465579,"id":"msg_7537d5ff-c943-47f6-9d8b-fc9f33220880","model":"chat-default","object":"chat.completion.chunk","system_fingerprint":null,"usage":{"billing_usage":{"claude_usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"claude_cache_creation_1_h_tokens":0,"claude_cache_creation_5_m_tokens":0,"input_tokens":14,"output_tokens":637},"semantic":"anthropic","source":"claude_messages"},"claude_cache_creation_1_h_tokens":0,"claude_cache_creation_5_m_tokens":0,"completion_tokens":637,"completion_tokens_details":{"audio_tokens":0,"image_tokens":0,"reasoning_tokens":0,"text_tokens":0},"input_tokens":14,"input_tokens_details":null,"output_tokens":0,"prompt_tokens":14,"prompt_tokens_details":{"audio_tokens":0,"cached_tokens":0,"image_tokens":0,"text_tokens":0},"total_tokens":651,"usage_semantic":"openai","usage_source":"anthropic"}}

data: [DONE]

# image
$ curl -s http://127.0.0.1:18080/v1/images/generations \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer agw_2Q2IFMTN2WFLL7AHYWOYUZJ6QV" \
  -d '{"model":"image-default","prompt":"一只像素风格的蓝色小鸟"}' \
  | jq -r '.data[0].b64_json' | base64 -d > out.png && open out.png

# base64 编码，图片内容在跟目录 out.png
DwLrnYzJ4S3enJyBJvjbUmk5pSjE46os4OAot+gNs1vhRGZTR+5yVaZWH7RQSiYlg6UuZTi4yGeYMUD1P5wRYBA5ZXLA1VaihjodlGzS8LWbiHCOLMWjqFSOFDcSYjlM61Kd3zg+cLATHaAN890Y5Sy/4YOIRix/y8167WDVtFLfZcfMkneR4TDo+4REA2Y61zUBm3MRn/2sKfvzZI2pEsqXIxI4hYnwt+xe2wIrFacvy6WUhaLvLlCYmNgPbNsTtrstaJIzuLb/ywMCSx5OPnMQnCQMdwIa4A+O3wFpqrjSC+Z1jaiYwqBxsvGCEloYIjbjNJ6p3wtuIshOS3Rm83NvNb9CIwVvi0XjlrCs/8pl5qaT+Luto8sqPG0lsN23FekCweOgm2pUsGuVxcpIgzhye4XrVteAaTC8T9ZM408fsfFWpJNQ8vnQJzkr1Ic7AttjdYBo9D9Io30pZZyMFVBTlyNhkqnWdCCIhD15K+hzlyqnLCLCfUx23CCvuM268Ebpglo9UZOXPOgPVemrDdIgEF4Rd+2+2WDTOMSR22jCVbGPwm2KxjHfvxzCFcaRWL1hcMknlceeYm+x1HzgnlNyOkbxaI8BQ7NpfqMI6uKBorce7ygDB/E7hyIbZVR2bd85L5wspryRpKh6KHbf+jg5CLUdnUvuwXXTl1GkDY/KYFhoFmkgmBAfWzJR9L1V5dbVWC316y3XjZOiokFYyzJNfhUEVJJc9OjTdewmxcNP80PaCggmFqbHR4jedaRspGpBmMznxXWuErNbUl9k0siw9vospZmBEYlnQWXu/JC3Dz5bRncKM+k0rjQB7J8FtX1J4Wa2fBX/c36cIokvI+nMloWqzznjZ6r7d2khMwRgbpio1KvnLhQSo+WDJQbjajk6nznsHDIxM7R9TCZ3QoTbskH5aNu1pWt2pUlYbE3NJwVmRLVOpXQQZjA19VsSMxTAYlo0uTdgg5KfWbNhTW1TaROthdgzf1tjoxPDnd6CukadW3KkqxBtcRfGUnR3QjOi7gDJtJ6QdxHYcknQWYgaISZL5HRZY7WuUAls98
```

#### 2.1.5 查看 token 用量

```shell
# 调用两次 chat 两次 image，显示 okay
$ curl -s http://127.0.0.1:19090/admin/v1/tokens/gtok_06fqms74111vcpsyevy316e5c8/usage | jq

{
  "from": null,
  "group_by": "",
  "groups": [
    {
      "key": "chat.completions",
      "requests": 2,
      "succeeded": 2,
      "failed": 0,
      "input_tokens": 28,
      "cached_input_tokens": 0,
      "output_tokens": 734,
      "reasoning_output_tokens": 702,
      "total_tokens": 1399,
      "images": 0,
      "usage_incomplete_records": 0
    },
    {
      "key": "images.generate",
      "requests": 2,
      "succeeded": 2,
      "failed": 0,
      "input_tokens": 0,
      "cached_input_tokens": 0,
      "output_tokens": 0,
      "reasoning_output_tokens": 0,
      "total_tokens": 0,
      "images": 2,
      "usage_incomplete_records": 0
    }
  ],
  "to": null,
  "token_id": "gtok_06fqms74111vcpsyevy316e5c8"
}
```

#### 2.1.6 切换 backend 为 dashscope

```shell
# 如上面 config 所示，返回的 chat-default 是 openai，现在切换成 dashscope
curl -s -X PUT http://127.0.0.1:19090/admin/v1/routes/chat-default/active-backend \
  -H 'Content-Type: application/json' \
  -H "If-Match: 1" \
  -d '{"backend_id":"dashscope-quickstart"}' | jq
  
{
  "active_backend": "dashscope-quickstart",
  "revision": 2
}

# 再次查看 config，发现以切换。
curl -s http://127.0.0.1:19090/admin/v1/config | jq

"routes": [
  {
    "id": "chat-default",
    "operation": "chat",
    "model_alias": "chat-default",
    "active_backend": "dashscope-quickstart",
    "targets": [
      {
        "backend_id": "openai-quickstart",
        "upstream_model": "qwen3.6-flash"
      },
      {
        "backend_id": "dashscope-quickstart",
        "upstream_model": "qwen-plus"
      }
    ]
  },
```

#### 2.1.7 再次调用 chat，发现返回的 model 是 qwen-plus

```shell
$ curl -s http://127.0.0.1:18080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer agw_2Q2IFMTN2WFLL7AHYWOYUZJ6QV" \
  -d '{"model":"chat-default","messages":[{"role":"user","content":"用一句话介绍自己"}]}' | jq

{
  "choices": [
    {
      "finish_reason": "stop",
      "index": 0,
      "message": {
        "content": "我是通义千问（Qwen），阿里巴巴集团旗下的超大规模语言模型，能够回答问题、创作文字，如写故事、写公文、写邮件、写剧本、逻辑推理、编程等，还能表达观点，玩游戏，支持多语言，并能根据你的需求提供帮助。",                                                                                    
        "role": "assistant"
      }
    }
  ],
  "created": 1784466058,
  "id": "chatcmpl-5be6ff77-e40a-9937-a612-e4f6f95b7dbb",
  "model": "chat-default",
  "object": "chat.completion",
  "usage": {
    "completion_tokens": 61,
    "prompt_tokens": 12,
    "prompt_tokens_details": {
      "cached_tokens": 0
    },
    "total_tokens": 73
  }
}
```

#### 2.1.8 审计信息

```shell
$ curl -s http://127.0.0.1:19090/admin/v1/audits | jq
{
  "data": [
    {
      "id": "aud_06fqmxsbc0yyxx35461d3fdfd4",
      "request_id": "req_06fqmxsbbzbj7x4300a1kgmx7w",
      "gateway_token_id": "gtok_06fqms74111vcpsyevy316e5c8",
      "operation": "chat.completions",
      "model_alias": "chat-default",
      "backend_id": "dashscope-quickstart",
      "provider": "dashscope",
      "config_revision": 2,
      "stream": false,
      "status": "succeeded",
      "http_status": 200,
      "fallback_count": 0,
      "usage": {
        "input_tokens": 12,
        "cached_input_tokens": 0,
        "output_tokens": 61,
        "total_tokens": 73
      },
      "started_at": 1784466058080,
      "finished_at": 1784466059753,
      "duration_ms": 1672,
      "upstream_duration_ms": 1649
    },
    ....
    
# 查看详细的 audit 信息
$ curl -s http://127.0.0.1:19090/admin/v1/audits/aud_06fqmxsbc0yyxx35461d3fdfd4 | jq

{
  "id": "aud_06fqmxsbc0yyxx35461d3fdfd4",
  "request_id": "req_06fqmxsbbzbj7x4300a1kgmx7w",
  "gateway_token_id": "gtok_06fqms74111vcpsyevy316e5c8",
  "operation": "chat.completions",
  "model_alias": "chat-default",
  "backend_id": "dashscope-quickstart",
  "provider": "dashscope",
  "config_revision": 2,
  "stream": false,
  "status": "succeeded",
  "http_status": 200,
  "fallback_count": 0,
  "usage": {
    "input_tokens": 12,
    "cached_input_tokens": 0,
    "output_tokens": 61,
    "total_tokens": 73
  },
  "started_at": 1784466058080,
  "finished_at": 1784466059753,
  "duration_ms": 1672,
  "upstream_duration_ms": 1649
}

```

#### 2.1.9 查看 credential

```shell
# 来自路由里配置的 credential_id，查看 credential 信息
$ curl -s http://127.0.0.1:19090/admin/v1/credentials | jq
{
  "data": [
    {
      "id": "dashscope-quickstart",
      "provider": "dashscope",
      "name": "dashscope-quickstart",
      "status": "active",
      "created_at": 1784464131996,
      "updated_at": 1784464131996
    },
    {
      "id": "openai-quickstart",
      "provider": "openai",
      "name": "openai-quickstart",
      "status": "active",
      "created_at": 1784464131982,
      "updated_at": 1784464131982
    }
  ]
}

$ curl -s http://127.0.0.1:19090/admin/v1/credentials/dashscope-quickstart | jq
{
  "id": "dashscope-quickstart",
  "provider": "dashscope",
  "name": "dashscope-quickstart",
  "status": "active",
  "created_at": 1784464131996,
  "updated_at": 1784464131996
}
```

#### 2.1.10 删除 token

```shell
# 查看 token 
$ curl -s http://127.0.0.1:19090/admin/v1/tokens | jq
{
  "data": [
    {
      "id": "gtok_06fqms74111vcpsyevy316e5c8",
      "name": "dev",
      "status": "active",
      "created_at": 1784464860168,
      "updated_at": 1784464860168,
      "expires_at": null,
      "last_used_at": 1784466058080
    }
  ]
}

# revoke
$ curl -s -X POST http://127.0.0.1:19090/admin/v1/tokens/gtok_06fqms74111vcpsyevy316e5c8/revoke | jq
{
  "status": "revoked"
}

$ curl -s http://127.0.0.1:19090/admin/v1/tokens | jq
{
  "data": [
    {
      "id": "gtok_06fqms74111vcpsyevy316e5c8",
      "name": "dev",
      "status": "revoked",
      "created_at": 1784464860168,
      "updated_at": 1784466579153,
      "expires_at": null,
      "revoked_at": 1784466579153,
      "last_used_at": 1784466058080
    }
  ]
}

# 调用，预期 401
$ curl -s http://127.0.0.1:18080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer agw_2Q2IFMTN2WFLL7AHYWOYUZJ6QV" \
  -d '{"model":"chat-default","messages":[{"role":"user","content":"用一句话介绍自己"}]}' | jq

{
  "error": {
    "message": "Gateway Token 无效",
    "type": "gateway_error",
    "param": null,
    "code": "invalid_gateway_token"
  }
}
```

## 3. 存在问题

1. 没有前端页面，只是演示用；
2. 没有做 jwt token 和 llms ak token 区分；服务端用 sha256 加密了；
3. 用 sqlite 做了 db，因此也没考虑可扩展性；
4. 支持导出 trace 
5. 没有做 Header 选择不同 router 的能力，由 schema 驱动配置 router；
6. 没有 key 级别的 lb；
7. 没有做缓存；
8. 
