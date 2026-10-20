package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"recall/internal/config"
)

type native struct{ http *http.Client }

func unixHTTP(socket string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		MaxConnsPerHost: 4,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func newNative(socket string) *native { return &native{http: unixHTTP(socket, 30*time.Second)} }

func post(ctx context.Context, c *http.Client, path string, input, output any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(input)
	if err != nil {
		return invalid("invalid inference request")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(b))
	if err != nil {
		return unavailable("cannot create inference request")
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := c.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return unavailable("local inference request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return unavailable("local inference returned an unsuccessful status")
	}
	if err := decodeJSON(res.Body, output); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return unavailable("invalid local inference response")
	}
	return nil
}

func decodeJSON(r io.Reader, out any) error {
	const limit = 1 << 20
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if len(b) > limit {
		return unavailable("local inference message is too large")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return unavailable("trailing response data")
	}
	return nil
}

func (n *native) Tokenize(ctx context.Context, text string) ([]int, error) {
	var out struct {
		Tokens []int `json:"tokens"`
	}
	err := post(ctx, n.http, "/tokenize", struct {
		Content    string `json:"content"`
		AddSpecial bool   `json:"add_special"`
	}{text, true}, &out)
	if err != nil {
		return nil, err
	}
	if out.Tokens == nil {
		return nil, unavailable("tokenizer omitted tokens")
	}
	for _, t := range out.Tokens {
		if t < 0 {
			return nil, unavailable("invalid token identifier")
		}
	}
	return out.Tokens, nil
}

func (n *native) Embed(ctx context.Context, purpose string, texts []string) ([][]float32, error) {
	if _, err := FormatInput(purpose, ""); err != nil {
		return nil, err
	}
	formatted := make([]string, len(texts))
	for i, text := range texts {
		formatted[i], _ = FormatInput(purpose, text)
		tokens, err := n.Tokenize(ctx, formatted[i])
		if err != nil {
			return nil, err
		}
		if len(tokens) > MaxTokens {
			return nil, invalid("embedding input exceeds 256 model tokens including prefix")
		}
	}
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(formatted); start += MaxBatch {
		end := min(start+MaxBatch, len(formatted))
		var out struct {
			Data []struct {
				Index     *int      `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		err := post(ctx, n.http, "/v1/embeddings", struct {
			Input    []string `json:"input"`
			Encoding string   `json:"encoding_format"`
		}{formatted[start:end], "float"}, &out)
		if err != nil {
			return nil, err
		}
		if len(out.Data) != end-start {
			return nil, unavailable("embedding response has wrong vector count")
		}
		batch := make([][]float32, end-start)
		for _, item := range out.Data {
			if item.Index == nil || *item.Index < 0 || *item.Index >= len(batch) || batch[*item.Index] != nil {
				return nil, unavailable("embedding response has invalid indices")
			}
			if err := normalize(item.Embedding); err != nil {
				return nil, err
			}
			batch[*item.Index] = item.Embedding
		}
		vectors = append(vectors, batch...)
	}
	return vectors, nil
}

func normalize(v []float32) error {
	if len(v) != Dimensions {
		return unavailable("embedding response has wrong dimensions")
	}
	var norm float64
	for _, f := range v {
		x := float64(f)
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return unavailable("embedding response is nonfinite")
		}
		norm += x * x
	}
	if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return unavailable("embedding response has invalid norm")
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return nil
}

func nativeArgs(cfg config.Config, socket string) []string {
	return []string{"--embedding", "--pooling", "mean", "--ctx-size", "2048", "--batch-size", "2048", "--ubatch-size", "512", "--parallel", "1", "--threads", "4", "--gpu-layers", "99", "--no-webui", "--offline", "--model", filepath.Join(cfg.ModelsDir, "nomic-embed-text-v1.5.f16.gguf"), "--host", socket}
}
