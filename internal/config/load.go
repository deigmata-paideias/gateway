package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

var ErrInvalidYAML = errors.New("config: invalid yaml")

func LoadBootstrap(path string) (Bootstrap, error) {
	var cfg Bootstrap
	if err := loadFile(path, &cfg); err != nil {
		return Bootstrap{}, err
	}
	applyBootstrapDefaults(&cfg)
	if err := ValidateBootstrap(cfg); err != nil {
		return Bootstrap{}, err
	}
	return cfg, nil
}

func LoadGateway(path string) (Gateway, error) {
	var cfg Gateway
	if err := loadFile(path, &cfg); err != nil {
		return Gateway{}, err
	}
	applyGatewayDefaults(&cfg)
	if err := ValidateGateway(cfg); err != nil {
		return Gateway{}, err
	}
	return cfg, nil
}

func DecodeGateway(data []byte) (Gateway, error) {
	var cfg Gateway
	if err := decodeStrict(data, &cfg); err != nil {
		return Gateway{}, err
	}
	applyGatewayDefaults(&cfg)
	if err := ValidateGateway(cfg); err != nil {
		return Gateway{}, err
	}
	return cfg, nil
}

func loadFile(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取配置 %q: %w", path, err)
	}
	if err := decodeStrict(data, dst); err != nil {
		return fmt.Errorf("解析配置 %q: %w", path, err)
	}
	return nil
}

func decodeStrict(data []byte, dst any) error {
	var root yaml.Node
	nodeDecoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := nodeDecoder.Decode(&root); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}
	if len(root.Content) == 0 {
		return fmt.Errorf("%w: 配置为空", ErrInvalidYAML)
	}
	if err := validateYAMLNode(root.Content[0]); err != nil {
		return err
	}
	var extra yaml.Node
	if err := nodeDecoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: 不允许多个 yaml document", ErrInvalidYAML)
		}
		return fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}
	return nil
}

func validateYAMLNode(node *yaml.Node) error {
	if node.Anchor != "" || node.Alias != nil {
		return fmt.Errorf("%w: 不允许 anchor 或 alias", ErrInvalidYAML)
	}
	if strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!") {
		return fmt.Errorf("%w: 不允许自定义 tag %q", ErrInvalidYAML, node.Tag)
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("%w: map key 必须是字符串", ErrInvalidYAML)
			}
			if _, ok := seen[key.Value]; ok {
				return fmt.Errorf("%w: 重复 key %q", ErrInvalidYAML, key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}
