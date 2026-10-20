// Package embedding supplies lazy, local inference shared by collection root.
package embedding

import (
	"context"
	"strings"

	"recall/internal/model"
)

const (
	Dimensions = 768
	MaxBatch   = 4
	MaxTokens  = 256
)

type Provider interface {
	Embed(ctx context.Context, purpose string, texts []string) ([][]float32, error)
	Tokenize(ctx context.Context, text string) ([]int, error)
}

// FormatInput is idempotent for an already formatted input of the same purpose.
func FormatInput(purpose, text string) (string, error) {
	var prefix string
	switch purpose {
	case "query":
		prefix = "search_query: "
	case "document":
		prefix = "search_document: "
	default:
		return "", invalid("embedding purpose must be query or document")
	}
	if strings.HasPrefix(text, prefix) {
		return text, nil
	}
	return prefix + text, nil
}

func invalid(message string) error { return &model.Error{Code: "invalid_argument", Message: message} }
func unavailable(message string) error {
	return &model.Error{Code: "runtime_unavailable", Message: message}
}
func assetInvalid(message string) error { return &model.Error{Code: "asset_invalid", Message: message} }
