package search

import (
	"context"
	"strings"
	"unicode/utf8"

	"recall/internal/embedding"
	"recall/internal/index"
	"recall/internal/model"
)

const (
	maxInputBytes   = 8192
	maxTitleBytes   = 1024
	maxOverlapBytes = 1024
)

// fittingSlice searches original rune boundaries; token IDs are never decoded
// back to text. The additional byte budget bounds low-token whitespace inputs.
func fittingSlice(ctx context.Context, p embedding.Provider, text string, maxBytes, budget int, format func(string) string) (int, error) {
	end := min(len(text), maxBytes)
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	boundaries := []int{0}
	for i := 0; i < end; {
		_, size := utf8.DecodeRuneInString(text[i:end])
		i += size
		boundaries = append(boundaries, i)
	}
	low, high := 0, len(boundaries)
	for low+1 < high {
		mid := (low + high) / 2
		tokens, err := p.Tokenize(ctx, format(text[:boundaries[mid]]))
		if err != nil {
			return 0, err
		}
		if len(tokens) <= budget {
			low = mid
		} else {
			high = mid
		}
	}
	return boundaries[low], nil
}

func chunks(ctx context.Context, p embedding.Provider, d model.Document) ([]index.Chunk, error) {
	titleEnd, err := fittingSlice(ctx, p, d.Title, maxTitleBytes, 64, func(s string) string { return s })
	if err != nil {
		return nil, err
	}
	title := d.Title[:titleEnd]
	format := func(body string) string {
		input, _ := embedding.FormatInput("document", title+"\n\n"+body)
		return input
	}
	base := 1
	if strings.HasSuffix(d.Raw, d.Body) {
		base += strings.Count(d.Raw[:len(d.Raw)-len(d.Body)], "\n")
	}
	if d.Body == "" {
		return []index.Chunk{{Text: title, Payload: title}}, nil
	}
	special, err := p.Tokenize(ctx, "")
	if err != nil {
		return nil, err
	}
	var result []index.Chunk
	for start := 0; start < len(d.Body); {
		length, err := fittingSlice(ctx, p, d.Body[start:], maxInputBytes-len(format("")), embedding.MaxTokens, format)
		if err != nil {
			return nil, err
		}
		if length == 0 {
			return nil, &model.Error{Code: "runtime_unavailable", Message: "tokenizer cannot fit a source passage"}
		}
		end := start + length
		result = append(result, index.Chunk{Text: d.Body[start:end], Payload: title + "\n\n" + d.Body[start:end], StartByte: start, EndByte: end,
			StartLine: base + strings.Count(d.Body[:start], "\n"), EndLine: base + strings.Count(d.Body[:end-1], "\n")})
		if end == len(d.Body) {
			break
		}
		// Find a suffix with at most 32 body tokens (excluding BOS/EOS).
		// Cap overlap bytes too, and force at least one rune of forward progress.
		next := max(start+1, end-maxOverlapBytes)
		for next < end && !utf8.RuneStart(d.Body[next]) {
			next++
		}
		bounds := []int{next}
		for i := next; i < end; {
			_, size := utf8.DecodeRuneInString(d.Body[i:end])
			i += size
			bounds = append(bounds, i)
		}
		lo, hi := 0, len(bounds)-1
		for lo < hi {
			mid := (lo + hi) / 2
			tok, err := p.Tokenize(ctx, d.Body[bounds[mid]:end])
			if err != nil {
				return nil, err
			}
			if len(tok) <= 32+len(special) {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		start = bounds[lo]
	}
	return result, nil
}
