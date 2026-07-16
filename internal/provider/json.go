package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deigmata-paideias/gateway/internal/model"
)

func RewriteTopLevel(data []byte, values map[string]string) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("%w: 解析 json: %v", ErrInvalidResponse, err)
	}
	for key, value := range values {
		if value != "" {
			object[key] = value
		}
	}
	result, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: 编码 json: %v", ErrInvalidResponse, err)
	}
	return result, nil
}

func RewriteString(data []byte, oldValue, newValue string) ([]byte, error) {
	if oldValue == "" || oldValue == newValue {
		return append([]byte(nil), data...), nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("%w: 解析 json: %v", ErrInvalidResponse, err)
	}
	rewriteValue(value, oldValue, newValue)
	result, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: 编码 json: %v", ErrInvalidResponse, err)
	}
	return result, nil
}

func rewriteValue(value any, oldValue, newValue string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok && text == oldValue {
				typed[key] = newValue
				continue
			}
			rewriteValue(child, oldValue, newValue)
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok && text == oldValue {
				typed[index] = newValue
				continue
			}
			rewriteValue(child, oldValue, newValue)
		}
	}
}

func PrepareRequest(data []byte, modelName, previousResponseID string, stream bool) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	object["model"] = modelName
	if previousResponseID != "" {
		object["previous_response_id"] = previousResponseID
	}
	object["stream"] = stream
	result, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return result, nil
}

func ExtractID(data []byte) string {
	var envelope struct {
		ID       string `json:"id"`
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return ""
	}
	if envelope.ID != "" {
		return envelope.ID
	}
	return envelope.Response.ID
}

func ExtractUsage(data []byte) model.Usage {
	var envelope struct {
		Usage struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			InputTokens      *int64 `json:"input_tokens"`
			OutputTokens     *int64 `json:"output_tokens"`
			TotalTokens      *int64 `json:"total_tokens"`
			PromptDetails    struct {
				CachedTokens *int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			InputDetails struct {
				CachedTokens *int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			CompletionDetails struct {
				ReasoningTokens *int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			OutputDetails struct {
				ReasoningTokens *int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return model.Usage{}
	}
	if len(envelope.Response.Usage) > 0 && string(envelope.Response.Usage) != "null" {
		wrapped := append([]byte(`{"usage":`), envelope.Response.Usage...)
		wrapped = append(wrapped, '}')
		return ExtractUsage(wrapped)
	}
	input := envelope.Usage.InputTokens
	if input == nil {
		input = envelope.Usage.PromptTokens
	}
	output := envelope.Usage.OutputTokens
	if output == nil {
		output = envelope.Usage.CompletionTokens
	}
	cached := envelope.Usage.InputDetails.CachedTokens
	if cached == nil {
		cached = envelope.Usage.PromptDetails.CachedTokens
	}
	reasoning := envelope.Usage.OutputDetails.ReasoningTokens
	if reasoning == nil {
		reasoning = envelope.Usage.CompletionDetails.ReasoningTokens
	}
	return model.Usage{
		InputTokens: input, CachedInputTokens: cached, OutputTokens: output,
		ReasoningOutputTokens: reasoning, TotalTokens: envelope.Usage.TotalTokens,
	}
}

func IsTerminalResponseEvent(eventType string) bool {
	return strings.HasSuffix(eventType, ".completed") || strings.HasSuffix(eventType, ".failed") ||
		strings.HasSuffix(eventType, ".incomplete") || eventType == "error"
}
