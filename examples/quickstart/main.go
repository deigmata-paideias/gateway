// quickstart_client 演示 Gateway Token、动态 Backend 切换和三类数据面调用。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxResponseBytes = 40 << 20

type options struct {
	adminURL  string
	dataURL   string
	provider  string
	operation string
	prompt    string
}

type client struct {
	http     *http.Client
	adminURL string
	dataURL  string
}

func main() {
	var cfg options
	flag.StringVar(&cfg.adminURL, "admin-url", "http://127.0.0.1:19090", "管理面地址")
	flag.StringVar(&cfg.dataURL, "data-url", "http://127.0.0.1:18080", "数据面地址")
	flag.StringVar(&cfg.provider, "provider", "openai", "目标 Provider：openai 或 dashscope")
	flag.StringVar(&cfg.operation, "operation", "chat", "调用能力：chat、responses、image 或 all")
	flag.StringVar(&cfg.prompt, "prompt", "用一句话介绍 schema 驱动的 AI 网关。", "发送给模型的提示词")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := run(ctx, cfg, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "quickstart 调用失败:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg options, output io.Writer) error {
	if cfg.provider != "openai" && cfg.provider != "dashscope" {
		return fmt.Errorf("provider 必须是 openai 或 dashscope")
	}
	operations, err := selectedOperations(cfg.operation)
	if err != nil {
		return err
	}
	if cfg.provider == "openai" && containsOperation(operations, "responses") {
		return errors.New("Bundled OpenAI Endpoint 未实现 Responses API；请使用 -provider dashscope")
	}
	if cfg.provider == "openai" && containsOperation(operations, "image") {
		return errors.New("Bundled OpenAI Endpoint 未提供图片模型；请使用 -provider dashscope")
	}
	c := client{
		http:     &http.Client{Timeout: 5 * time.Minute},
		adminURL: strings.TrimRight(cfg.adminURL, "/"),
		dataURL:  strings.TrimRight(cfg.dataURL, "/"),
	}
	token, err := c.createToken(ctx)
	if err != nil {
		return err
	}
	if err := c.selectProvider(ctx, cfg.provider, operations); err != nil {
		return err
	}
	if err := c.printModels(ctx, token, output); err != nil {
		return err
	}
	for _, operation := range operations {
		if err := c.invoke(ctx, token, operation, cfg.prompt, output); err != nil {
			return err
		}
	}
	return nil
}

func containsOperation(operations []string, target string) bool {
	for _, operation := range operations {
		if operation == target {
			return true
		}
	}
	return false
}

func selectedOperations(operation string) ([]string, error) {
	switch operation {
	case "chat", "responses", "image":
		return []string{operation}, nil
	case "all":
		return []string{"chat", "responses", "image"}, nil
	default:
		return nil, fmt.Errorf("operation 必须是 chat、responses、image 或 all")
	}
}

func (c client) createToken(ctx context.Context) (string, error) {
	var result struct {
		Token string `json:"token"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.adminURL+"/admin/v1/tokens", "", map[string]any{
		"name": fmt.Sprintf("quickstart-%d", time.Now().Unix()),
	}, &result); err != nil {
		return "", fmt.Errorf("创建 Gateway Token: %w", err)
	}
	if result.Token == "" {
		return "", errors.New("创建 Gateway Token 的响应缺少 token")
	}
	return result.Token, nil
}

func (c client) selectProvider(ctx context.Context, provider string, operations []string) error {
	var current struct {
		Revision int64 `json:"revision"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.adminURL+"/admin/v1/config", "", nil, &current); err != nil {
		return fmt.Errorf("查询配置 Revision: %w", err)
	}
	backendID := provider + "-quickstart"
	for _, operation := range operations {
		routeID := operation + "-default"
		var switched struct {
			Revision int64 `json:"revision"`
		}
		headers := map[string]string{"If-Match": fmt.Sprint(current.Revision)}
		if err := c.doJSONWithHeaders(
			ctx,
			http.MethodPut,
			c.adminURL+"/admin/v1/routes/"+routeID+"/active-backend",
			"",
			map[string]string{"backend_id": backendID},
			headers,
			&switched,
		); err != nil {
			return fmt.Errorf("切换 %s 到 %s: %w", routeID, backendID, err)
		}
		current.Revision = switched.Revision
	}
	return nil
}

func (c client) printModels(ctx context.Context, token string, output io.Writer) error {
	var result json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, c.dataURL+"/v1/models", token, nil, &result); err != nil {
		return fmt.Errorf("查询模型别名: %w", err)
	}
	return printJSON(output, "models", result)
}

func (c client) invoke(ctx context.Context, token, operation, prompt string, output io.Writer) error {
	var path string
	var request map[string]any
	switch operation {
	case "chat":
		path = "/v1/chat/completions"
		request = map[string]any{
			"model":    "chat-default",
			"messages": []map[string]string{{"role": "user", "content": prompt}},
		}
	case "responses":
		path = "/v1/responses"
		request = map[string]any{"model": "responses-default", "input": prompt}
	case "image":
		path = "/v1/images/generations"
		request = map[string]any{"model": "image-default", "prompt": prompt, "n": 1, "size": "512x512"}
	default:
		return fmt.Errorf("不支持的 operation %q", operation)
	}
	var response json.RawMessage
	if err := c.doJSON(ctx, http.MethodPost, c.dataURL+path, token, request, &response); err != nil {
		return fmt.Errorf("%s 调用: %w", operation, err)
	}
	if operation == "image" {
		return printImageSummary(output, response)
	}
	return printJSON(output, operation, response)
}

func (c client) doJSON(
	ctx context.Context,
	method,
	url,
	token string,
	requestBody any,
	responseBody any,
) error {
	return c.doJSONWithHeaders(ctx, method, url, token, requestBody, nil, responseBody)
}

func (c client) doJSONWithHeaders(
	ctx context.Context,
	method,
	url,
	token string,
	requestBody any,
	headers map[string]string,
	responseBody any,
) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("编码请求: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("创建请求: %w", err)
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("执行请求: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("读取响应: %w", err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("响应超过 %d 字节", maxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, data)
	}
	if err := json.Unmarshal(data, responseBody); err != nil {
		return fmt.Errorf("解析响应: %w", err)
	}
	return nil
}

func printJSON(output io.Writer, name string, raw json.RawMessage) error {
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		return fmt.Errorf("格式化 %s 响应: %w", name, err)
	}
	_, err := fmt.Fprintf(output, "\n[%s]\n%s\n", name, formatted.Bytes())
	return err
}

func printImageSummary(output io.Writer, raw json.RawMessage) error {
	var response struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("解析图片响应: %w", err)
	}
	if len(response.Data) == 0 {
		return errors.New("图片响应为空")
	}
	for index, item := range response.Data {
		decoded, err := base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil {
			return fmt.Errorf("第 %d 张图片 Base64 无效: %w", index+1, err)
		}
		digest := sha256.Sum256(decoded)
		if _, err := fmt.Fprintf(
			output,
			"\n[image %d]\nbytes=%d sha256=%s\n",
			index+1,
			len(decoded),
			hex.EncodeToString(digest[:]),
		); err != nil {
			return err
		}
	}
	return nil
}
