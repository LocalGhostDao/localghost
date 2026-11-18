package oracled

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamChatCarriesHistory pins the conversation contract: prior turns reach llama-server as
// messages, oldest first, with the current prompt last and the think instruction wrapping ONLY
// the current prompt. Before this, every turn shipped as a lone user message.
func TestStreamChatCarriesHistory(t *testing.T) {
	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
		Kwargs struct {
			EnableThinking *bool `json:"enable_thinking"`
		} `json:"chat_template_kwargs"`
		MaxTokens int `json:"max_tokens"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("payload: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	b := &llamaBackend{addr: strings.TrimPrefix(srv.URL, "http://"), streamClient: srv.Client(), cfg: LlamaConfig{ModelName: "test"}}
	history := []Turn{
		{Role: "user", Content: "name three rivers"},
		{Role: "assistant", Content: "Thames, Danube, Nile."},
		{Role: "system", Content: "must be dropped"},
		{Role: "user", Content: ""},
	}
	out, model, err := b.StreamChat(context.Background(), history, "which is longest?", "brief", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if model != "test" {
		t.Fatalf("model = %q", model)
	}
	if !got.Stream {
		t.Fatal("stream flag lost")
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (two prior + current): %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "user" || got.Messages[0].Content != "name three rivers" {
		t.Fatalf("turn 0 = %+v", got.Messages[0])
	}
	if got.Messages[1].Role != "assistant" || got.Messages[1].Content != "Thames, Danube, Nile." {
		t.Fatalf("turn 1 = %+v", got.Messages[1])
	}
	last, _ := got.Messages[2].Content.(string)
	if got.Messages[2].Role != "user" || !strings.HasSuffix(last, "which is longest?") || !strings.Contains(last, "Think briefly") {
		t.Fatalf("current turn must carry the think instruction and the prompt: %+v", got.Messages[2])
	}
	if got.Kwargs.EnableThinking == nil || !*got.Kwargs.EnableThinking || got.MaxTokens != 2048 {
		t.Fatalf("brief: thinking %v budget %d", got.Kwargs.EnableThinking, got.MaxTokens)
	}
	// off: the native reasoning channel is closed and the prompt goes as it is
	got.MaxTokens = 0
	out, _, err = b.StreamChat(context.Background(), nil, "which is longest?", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if got.Kwargs.EnableThinking == nil || *got.Kwargs.EnableThinking || got.MaxTokens != 0 {
		t.Fatalf("off: thinking %v budget %d", got.Kwargs.EnableThinking, got.MaxTokens)
	}
	if c, _ := got.Messages[0].Content.(string); c != "which is longest?" {
		t.Fatalf("off: prompt %q", c)
	}
	// deep: the channel open, the long instruction, the big budget
	out, _, err = b.StreamChat(context.Background(), nil, "which is longest?", "deep", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if got.Kwargs.EnableThinking == nil || !*got.Kwargs.EnableThinking || got.MaxTokens != 8192 {
		t.Fatalf("deep: thinking %v budget %d", got.Kwargs.EnableThinking, got.MaxTokens)
	}
	if c, _ := got.Messages[0].Content.(string); !strings.Contains(c, "at length") {
		t.Fatalf("deep: prompt %q", c)
	}
	// a long answer after a brief think: the caller's room, not the brief think's 2,048
	got.MaxTokens = 0
	out, _, err = b.StreamChatBudget(context.Background(), nil, "tell me the whole story", "brief", "", 6144)
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if got.MaxTokens != 6144 {
		t.Fatalf("caller's budget: %d", got.MaxTokens)
	}
	// bounded: no caller gets more than the cap, and a negative is no budget of its own
	out, _, err = b.StreamChatBudget(context.Background(), nil, "x", "deep", "", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if got.MaxTokens != maxChatTokens {
		t.Fatalf("capped: %d", got.MaxTokens)
	}
	out, _, err = b.StreamChatBudget(context.Background(), nil, "x", "deep", "", -5)
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if got.MaxTokens != 8192 {
		t.Fatalf("negative: %d", got.MaxTokens)
	}
}
