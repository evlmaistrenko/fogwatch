package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAITool(t *testing.T) {
	if NewAI("https://api.openai.com/v1", "k", "m").tool["type"] != "web_search" {
		t.Fatal("openai tool")
	}
	a := NewAI("https://openrouter.ai/api/v1/", "k", "m")
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
	got, err := NewAI(srv.URL+"/v1", "k", "m").Fetch(context.Background(), "2026-10-09", []string{"OLD01", "OLD02"})
	if err != nil || len(got) != 1 || got[0].Code != "ABCD1" || got[0].RewardRU != "ОК" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestScheduledFetchAlerts(t *testing.T) {
	// Fake Telegram: collect sendMessage texts.
	var sent []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct{ Text string }
		json.NewDecoder(r.Body).Decode(&m)
		sent = append(sent, m.Text)
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()
	// Fake LLM: fails while broken is true.
	broken := true
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"codes\":[]}"}]}]}`))
	}))
	defer llm.Close()

	st, err := OpenStore(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{
		tg:     &TG{base: tg.URL + "/", hc: tg.Client()},
		st:     st,
		ai:     NewAI(llm.URL, "k", "m"),
		admins: map[int64]bool{42: true},
		loc:    time.UTC,
	}
	ctx := context.Background()

	b.ScheduledFetch(ctx) // failure 1: below threshold
	if len(sent) != 0 {
		t.Fatalf("alerted too early: %q", sent)
	}
	b.ScheduledFetch(ctx) // failure 2: alert
	b.ScheduledFetch(ctx) // failure 3: no repeat
	if len(sent) != 1 || !strings.Contains(sent[0], "2 attempts") || strings.Contains(sent[0], "502") {
		t.Fatalf("alert: %q", sent)
	}
	broken = false
	b.ScheduledFetch(ctx) // recovered: one notice
	b.ScheduledFetch(ctx) // still fine: nothing
	if len(sent) != 2 || !strings.Contains(sent[1], "works again") {
		t.Fatalf("recovery: %q", sent)
	}
}
