package search

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"recall/internal/embedding"
	"recall/internal/model"
)

// Rune tokens plus BOS/EOS deliberately make byte counts an invalid proxy.
type runeProvider struct{}

func (runeProvider) Tokenize(_ context.Context, s string) ([]int, error) {
	return make([]int, utf8.RuneCountInString(s)+2), nil
}
func (runeProvider) Embed(context.Context, string, []string) ([][]float32, error) {
	panic("unexpected embedding")
}

func TestChunksBudgetOverlapAndSourceLines(t *testing.T) {
	body := strings.Repeat("αβγ\r\n", 180)
	d := model.Document{Title: strings.Repeat("界", 100), Body: body, Raw: "---\ntitle: example\n---\n" + body}
	got, err := chunks(t.Context(), runeProvider{}, d)
	if err != nil || len(got) < 2 {
		t.Fatalf("chunks=%d err=%v", len(got), err)
	}
	end := 0
	for i, c := range got {
		if c.StartByte > end || c.EndByte <= end || c.Text != body[c.StartByte:c.EndByte] || !utf8.ValidString(c.Text) {
			t.Fatalf("coverage at %d: %+v previous end=%d", i, c, end)
		}
		if i > 0 && c.StartByte >= end {
			t.Fatal("missing overlap")
		}
		if i > 0 && utf8.RuneCountInString(body[c.StartByte:end]) != 32 {
			t.Fatal("body overlap must be 32 tokenizer tokens, excluding special tokens")
		}
		title, _, ok := strings.Cut(c.Payload, "\n\n")
		if !ok || title != strings.Repeat("界", 62) {
			t.Fatal("title exceeded 64 raw tokenizer tokens including BOS/EOS")
		}
		formatted, _ := embedding.FormatInput("document", c.Payload)
		tokens, _ := (runeProvider{}).Tokenize(t.Context(), formatted)
		if len(tokens) > 256 || strings.HasPrefix(c.Payload, "search_document: ") {
			t.Fatalf("budget/prefix: %d %q", len(tokens), c.Payload)
		}
		if c.StartLine != 4+strings.Count(body[:c.StartByte], "\n") || c.EndLine != 4+strings.Count(body[:c.EndByte-1], "\n") {
			t.Fatalf("wrong source citation: %+v", c)
		}
		end = c.EndByte
	}
	if end != len(body) {
		t.Fatal("lost body suffix")
	}
}

type separatorProvider struct{ runeProvider }

func (separatorProvider) Tokenize(_ context.Context, s string) ([]int, error) {
	return make([]int, len(strings.Fields(s))+2), nil
}

func TestChunksBoundRawSeparatorsAndTitleOnly(t *testing.T) {
	for _, body := range []string{"", strings.Repeat(" \t\r\n", 25000) + "tail"} {
		got, err := chunks(t.Context(), separatorProvider{}, model.Document{Title: strings.Repeat(" ", 20000) + "title", Body: body, Raw: body})
		if err != nil || len(got) == 0 {
			t.Fatalf("%d %v", len(got), err)
		}
		end := 0
		for _, c := range got {
			formatted, _ := embedding.FormatInput("document", c.Payload)
			if len(formatted) > 8192 || c.StartByte > end || (body != "" && c.EndByte <= end) {
				t.Fatalf("unbounded/nonprogress chunk: %+v", c)
			}
			end = c.EndByte
		}
		if end != len(body) {
			t.Fatal("lost source coverage")
		}
	}
}
