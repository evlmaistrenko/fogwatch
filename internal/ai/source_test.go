package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchPage(t *testing.T) {
	pages := map[string]string{
		"/meta":   `<meta property="article:published_time" content="2026-06-01T09:00:00+00:00"><b>GETTHERE</b>`,
		"/meta2":  `<meta content="2026-06-02" name="date">`,
		"/ld":     `<script type="application/ld+json">{"@type":"NewsArticle","datePublished": "2026-06-03T00:00:00Z"}</script>`,
		"/nodate": `<p>code: abcd-12</p>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pages[r.URL.Path]))
	}))
	defer srv.Close()
	for path, want := range map[string]string{"/meta": "2026-06-01", "/meta2": "2026-06-02", "/ld": "2026-06-03", "/nodate": ""} {
		p, err := FetchPage(context.Background(), srv.URL+path)
		if err != nil || p.Published != want {
			t.Errorf("%s: published %q, %v", path, p.Published, err)
		}
	}
	p, _ := FetchPage(context.Background(), srv.URL+"/meta")
	if !p.Has("getthere") || p.Has("GETTHER") || p.Has("ETTHERE") {
		t.Error("Has: whole words only, any case")
	}
	if p, _ := FetchPage(context.Background(), srv.URL+"/nodate"); !p.Has("ABCD-12") || p.Has("ABCD") {
		t.Error("Has: dashes are part of a code")
	}
}
