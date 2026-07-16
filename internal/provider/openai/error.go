package openai

import (
	"errors"
	"fmt"
	"net/http"

	openaisdk "github.com/openai/openai-go/v3"

	"github.com/deigmata-paideias/gateway/internal/provider"
)

func mapError(err error) error {
	var apiError *openaisdk.Error
	if errors.As(err, &apiError) {
		status := apiError.StatusCode
		return &provider.Error{
			Status: status, Code: "upstream_error", Retryable: status == http.StatusTooManyRequests || status >= 500,
			Err: fmt.Errorf("openai api error"),
		}
	}
	return &provider.Error{Status: 0, Code: "upstream_unavailable", Retryable: true, Err: err}
}
