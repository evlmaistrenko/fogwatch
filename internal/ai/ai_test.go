package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAITool(t *testing.T) {
	if New("https://api.openai.com/v1", "k", "m").tool["type"] != "web_search" {
		t.Fatal("openai tool")
	}
	a := New("https://openrouter.ai/api/v1/", "k", "m")
	if a.tool["type"] != "openrouter:web_search" || a.url != "https://openrouter.ai/api/v1/responses" {
		t.Fatalf("openrouter: %v %s", a.tool, a.url)
	}
}

func TestAIFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer k" || req["model"] != "m" {
			t.Errorf("bad request: %s %v", r.URL.Path, req["model"])
		}
		if in, _ := req["input"].(string); !strings.Contains(in, "do not return them: OLD01, OLD02.") {
			t.Errorf("known codes missing from prompt: %q", in)
		}
		// Model wrapped the JSON in a code fence: must still parse.
		w.Write([]byte(`{"status":"completed","output":[
			{"type":"web_search_call"},
			{"type":"message","content":[{"type":"output_text","text":"` +
			"```json\\n{\\\"codes\\\":[{\\\"code\\\":\\\"ABCD1\\\",\\\"reward_en\\\":\\\"BP\\\",\\\"reward_ru\\\":\\\"ОК\\\",\\\"expires\\\":\\\"\\\",\\\"source_url\\\":\\\"https://x\\\"}]}\\n```" +
			`"}]}]}`))
	}))
	defer srv.Close()
	got, err := New(srv.URL+"/v1", "k", "m").Fetch(context.Background(), "2026-10-09", []string{"OLD01", "OLD02"})
	if err != nil || len(got) != 1 || got[0].Code != "ABCD1" || got[0].RewardRU != "ОК" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
