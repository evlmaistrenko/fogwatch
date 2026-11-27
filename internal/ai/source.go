package ai

// Source pages are re-read by the bot to check what the model claims about
// them: models invent codes and dates, the page itself doesn't.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Page struct {
	html      string
	Published string // YYYY-MM-DD from the page's metadata, or ""
}

var (
	pageClient = &http.Client{Timeout: 30 * time.Second}
	// <meta property="article:published_time" content="2026-06-01T…">, either
	// attribute order, and JSON-LD "datePublished": "2026-06-01…".
	publishedRe = []*regexp.Regexp{
		regexp.MustCompile(`(?i)<meta[^>]+(?:property|name)=["'](?:article:published_time|date|pubdate)["'][^>]+content=["'](\d{4}-\d{2}-\d{2})`),
		regexp.MustCompile(`(?i)<meta[^>]+content=["'](\d{4}-\d{2}-\d{2})[^"']*["'][^>]+(?:property|name)=["'](?:article:published_time|date|pubdate)["']`),
		regexp.MustCompile(`"datePublished"\s*:\s*"(\d{4}-\d{2}-\d{2})`),
	}
)

const maxPage = 4 << 20

// FetchPage downloads a source page (at most maxPage bytes).
func FetchPage(ctx context.Context, url string) (*Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; fogwatch)")
	resp, err := pageClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source page: http %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil {
		return nil, err
	}
	p := &Page{html: string(raw)}
	for _, re := range publishedRe {
		if m := re.FindStringSubmatch(p.html); m != nil {
			p.Published = m[1]
			break
		}
	}
	return p, nil
}

// Has reports whether code appears on the page as a whole word, ignoring
// case. The raw HTML is searched, so codes in scripts and JSON count too.
func (p *Page) Has(code string) bool {
	h, c := strings.ToUpper(p.html), strings.ToUpper(code)
	for i := 0; ; {
		j := strings.Index(h[i:], c)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(c)
		if (j == 0 || !codeChar(h[j-1])) && (end == len(h) || !codeChar(h[end])) {
			return true
		}
		i = j + 1
	}
}

func codeChar(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}
