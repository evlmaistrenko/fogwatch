package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fogwatch/internal/ai"
	"fogwatch/internal/store"
	"fogwatch/internal/telegram"
)

func TestCodeMsg(t *testing.T) {
	text, kb := codeMsg("ru", "🆕", store.Code{Code: "AAAA1", RewardEN: "BP", RewardRU: "ОК", Expires: "2030-01-01"})
	if text != "🆕 <code>AAAA1</code> — ОК · до 2030-01-01" {
		t.Fatalf("text: %s", text)
	}
	if b := kb.InlineKeyboard[0][0]; b.CopyText == nil || b.CopyText.Text != "AAAA1" || !strings.HasPrefix(b.Text, "📋") {
		t.Fatalf("button: %+v", b)
	}
}

func TestModCard(t *testing.T) {
	text, kb := modCard("en", store.Code{Code: "AAAA1", RewardEN: "BP", URL: "https://x.test/a?b=1&c=2"})
	if !strings.Contains(text, "https://x.test/a?b=1&amp;c=2") {
		t.Fatalf("url not escaped: %s", text)
	}
	if len(kb.InlineKeyboard) != 2 || kb.InlineKeyboard[1][0].CallbackData != "ok:AAAA1" ||
		kb.InlineKeyboard[1][1].CallbackData != "no:AAAA1" {
		t.Fatalf("keyboard: %+v", kb)
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

	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(telegram.NewAt(tg.URL+"/"), st, ai.New(llm.URL, "k", "m"), map[int64]bool{42: true}, time.UTC, "test")
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

func TestModerationSkipsAdmins(t *testing.T) {
	// Fake Telegram: collect method + chat_id + text.
	type call struct {
		method string
		chat   int64
		text   string
	}
	var calls []call
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ChatID int64 `json:"chat_id"`
			Text   string
		}
		json.NewDecoder(r.Body).Decode(&m)
		calls = append(calls, call{r.URL.Path[1:], m.ChatID, m.Text})
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(telegram.NewAt(tg.URL+"/"), st, nil, map[int64]bool{42: true}, time.UTC, "test")
	ctx := context.Background()
	st.Touch(42, "en")
	st.Touch(7, "en")

	c := store.Code{Code: "AAAA1", Source: "ai"}
	b.broadcast(ctx, []store.Code{c}, true)
	if len(calls) != 1 || calls[0].chat != 7 {
		t.Fatalf("skipAdmins: %+v", calls)
	}
	calls = nil
	b.broadcast(ctx, []store.Code{c}, false)
	if len(calls) != 2 {
		t.Fatalf("all: %+v", calls)
	}

	// Stale card: the code was already published, the click shows that.
	st.Upsert(c)
	st.Moderate("AAAA1", store.StatusPublished)
	calls = nil
	q := &telegram.CallbackQuery{ID: "q", From: telegram.User{ID: 42}, Message: &telegram.Message{MessageID: 5, Chat: telegram.Chat{ID: 42}}}
	b.onModerate(ctx, q, false, "AAAA1")
	if len(calls) != 2 || calls[1].method != "editMessageText" || !strings.HasSuffix(calls[1].text, "✅ Published") {
		t.Fatalf("stale card: %+v", calls)
	}
}

func TestHelpShowsVersion(t *testing.T) {
	b := New(nil, nil, nil, nil, time.UTC, "1.2.3<x>")
	for _, admin := range []bool{false, true} {
		if h := b.help("en", admin); !strings.HasSuffix(h, "<i>Version 1.2.3&lt;x&gt;</i>") {
			t.Fatalf("admin=%v: %q", admin, h)
		}
	}
}
