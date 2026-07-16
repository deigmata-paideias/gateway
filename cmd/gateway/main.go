package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/deigmata-paideias/gateway/internal/app"
)

const version = "0.1.0"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(os.Args[1:], logger); err != nil {
		logger.Error("ai-gateway 退出", "error", err)
		os.Exit(1)
	}
}

func run(args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("用法: ai-gateway serve|healthcheck|version")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], logger)
	case "healthcheck":
		return healthcheck(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("未知子命令 %q", args[0])
	}
}

func serve(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	bootstrapPath := flags.String("bootstrap-config", "configs/bootstrap.example.yaml", "Bootstrap YAML 路径")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	application, err := app.Build(ctx, *bootstrapPath, logger)
	if err != nil {
		return fmt.Errorf("构建网关: %w", err)
	}
	return application.Run(ctx)
}

func healthcheck(args []string) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	endpoint := flags.String("url", "http://127.0.0.1:8081/readyz", "健康检查 URL")
	timeout := flags.Duration("timeout", 2*time.Second, "请求超时")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, *endpoint, nil)
	if err != nil {
		return fmt.Errorf("创建健康检查请求: %w", err)
	}
	client := &http.Client{Timeout: *timeout}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("执行健康检查: %w", err)
	}
	defer response.Body.Close()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return fmt.Errorf("解析健康检查响应: %w", err)
	}
	if response.StatusCode != http.StatusOK || body.Status != "ready" {
		return fmt.Errorf("服务未就绪: status=%d state=%q", response.StatusCode, body.Status)
	}
	return nil
}
