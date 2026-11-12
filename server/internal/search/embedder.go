package search

// Embedding client (spec 6): plain HTTP+JSON to a llama.cpp /v1/embeddings endpoint, stdlib only.
// Vectors are normalised to unit length in Go before storage (cosine == dot thereafter, and it
// protects against a runtime returning unnormalised output). Vectors travel to Postgres in pgvector's
// text format, so the poltergres client needs nothing new.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Embedder struct {
	BaseURL string // e.g. http://127.0.0.1:18081
	ModelID string // recorded on every row (search.chunks.emb_model)
	HC      *http.Client
	// Key is the embedding server's own (EmbedServer.Key), sent as a bearer; "" for an
	// external endpoint, which gets none of the box's.
	Key string
}

func NewEmbedder(baseURL, modelID string) *Embedder {
	return &Embedder{BaseURL: baseURL, ModelID: modelID, HC: &http.Client{Timeout: 60 * time.Second}}
}

// Embed returns one unit-normalised vector per input text.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.Key != "" {
		req.Header.Set("Authorization", "Bearer "+e.Key)
	}
	resp, err := e.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// the server's own words: "input is too large to process" and friends are the reason
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return nil, &EmbedHTTPError{Code: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = normalize(d.Embedding)
	}
	return vecs, nil
}

// EmbedHTTPError is the embeddings server refusing a request: it was reached, and said no.
type EmbedHTTPError struct {
	Code int
	Msg  string
}

func (e *EmbedHTTPError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("embeddings: http %d", e.Code)
	}
	return fmt.Sprintf("embeddings: http %d: %s", e.Code, e.Msg)
}

// EmbedFitting is Embed for a batch that may hold an input the server refuses (too long for one
// physical batch, or its context). The batch is tried whole; when the server says no, each input
// is tried alone, and one it still refuses is cut to half its length, then half again, up to five
// times, so its vector is of its beginning rather than nothing at all (full-text search still
// has the whole of it). An unreachable server is not retried input by input. cut counts the
// inputs embedded from a shortened text.
func (e *Embedder) EmbedFitting(ctx context.Context, texts []string) (vecs [][]float32, cut int, err error) {
	vecs, err = e.Embed(ctx, texts)
	var he *EmbedHTTPError
	if err == nil || !errors.As(err, &he) {
		return vecs, 0, err
	}
	vecs = make([][]float32, len(texts))
	for i, t := range texts {
		v, err := e.Embed(ctx, []string{t})
		for halves := 0; err != nil && errors.As(err, &he) && halves < 5; halves++ {
			r := []rune(t)
			if len(r) < 16 {
				break
			}
			t = string(r[:len(r)/2])
			v, err = e.Embed(ctx, []string{t})
			if err == nil {
				cut++
			}
		}
		if err != nil {
			return nil, cut, err
		}
		vecs[i] = v[0]
	}
	return vecs, cut, nil
}

func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	n := math.Sqrt(sum)
	if n == 0 {
		return v
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
	return v
}

// VecText renders a vector in pgvector's text input format: [0.1,0.2,...].
func VecText(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
