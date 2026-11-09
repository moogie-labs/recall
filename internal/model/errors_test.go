package model

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestErrorCode(t *testing.T) {
	for _, code := range []string{
		"not_initialized", "invalid_argument", "invalid_document",
		"duplicate_id", "not_found", "already_exists", "conflict", "outside_root",
		"folder_not_empty", "index_unavailable", "indexing", "runtime_unavailable",
		"asset_invalid", "unsupported_platform",
	} {
		t.Run(code, func(t *testing.T) {
			err := fmt.Errorf("operation failed: %w", &Error{Code: code, Message: "safe explanation", Path: "folder/ideas.note.md"})
			if got := ErrorCode(err); got != code {
				t.Fatalf("ErrorCode = %q, want %q", got, code)
			}
			if !strings.Contains(err.Error(), "safe explanation") || !strings.Contains(err.Error(), "folder/ideas.note.md") {
				t.Fatalf("error is not actionable: %v", err)
			}
		})
	}
	if got := ErrorCode(errors.New("unexpected")); got != "internal" {
		t.Fatalf("unexpected error code = %q", got)
	}
	if got := ErrorCode(nil); got != "" {
		t.Fatalf("nil error code = %q", got)
	}
}

func TestPublicDTOsUseExplicitJSONNames(t *testing.T) {
	values := []any{Ref{}, Document{}, Diagnostic{}, Snapshot{}, Filter{}, SearchQuery{}, Hit{}, SyncReport{}, Error{}}
	for _, value := range values {
		typeOf := reflect.TypeOf(value)
		for i := 0; i < typeOf.NumField(); i++ {
			field := typeOf.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || strings.ContainsAny(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
				t.Errorf("%s.%s needs an explicit lower_snake_case JSON name", typeOf.Name(), field.Name)
			}
		}
	}
}
