package ai

// Responses API (OpenAI or a compatible provider such as OpenRouter) with
// web search and structured JSON output.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	url, key, model string
	tool            map[string]any // web search tool, provider-specific
	hc              *http.Client
}

// New: baseURL like https://api.openai.com/v1 or https://openrouter.ai/api/v1.
func New(baseURL, key, model string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	// OpenAI's built-in tool vs OpenRouter's server tool (works with any model).
	tool := map[string]any{"type": "web_search"}
	if strings.Contains(baseURL, "openrouter.ai") {
		tool = map[string]any{"type": "openrouter:web_search"}
	}
	return &Client{
		url: baseURL + "/responses", key: key, model: model, tool: tool,
		hc: &http.Client{Timeout: 5 * time.Minute},
	}
}

type Found struct {
	Code      string `json:"code"`
	RewardEN  string `json:"reward_en"`
	RewardRU  string `json:"reward_ru"`
	Expires   string `json:"expires"`
	SourceURL string `json:"source_url"`
}

var foundSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"codes"},
	"properties": map[string]any{
		"codes": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"code", "reward_en", "reward_ru", "expires", "source_url"},
				"properties": map[string]any{
					"code":       map[string]any{"type": "string"},
					"reward_en":  map[string]any{"type": "string"},
					"reward_ru":  map[string]any{"type": "string"},
					"expires":    map[string]any{"type": "string", "description": "YYYY-MM-DD or empty if unknown"},
					"source_url": map[string]any{"type": "string"},
				},
			},
		},
	},
}

const prompt = `Today is %s. Search the web for Dead by Daylight promo codes that can currently be redeemed in the game's store.

Rules:
- Include only codes a source explicitly lists as active/working (recently updated page, or a stated expiry date in the future).
- Never invent, guess or complete codes. Copy them exactly as published.
- Skip codes marked expired.
- reward_en / reward_ru: very short reward description (e.g. "50 000 Bloodpoints" / "50 000 очков крови").
- expires: YYYY-MM-DD if a source states it, otherwise "".
- source_url: the page where you saw the code.
- Return an empty list if nothing reliable is found.`

// knownPrompt is appended when the database already has codes: the model
// should spend its search on new ones (duplicates are dropped anyway).
const knownPrompt = `

Already known codes, do not return them: %s.
Return only codes that are not in this list; an empty list if there are none.`

// Fetch searches for codes. known = codes already in the database (any status).
func (a *Client) Fetch(ctx context.Context, today string, known []string) ([]Found, error) {
	input := fmt.Sprintf(prompt, today)
	if len(known) > 0 {
		input += fmt.Sprintf(knownPrompt, strings.Join(known, ", "))
	}
	body, _ := json.Marshal(map[string]any{
		"model": a.model,
		"input": input,
		"tools": []any{a.tool},
		"text": map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "dbd_codes", "strict": true, "schema": foundSchema,
		}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		if len(raw) > 300 {
			raw = raw[:300]
		}
		return nil, fmt.Errorf("openai %d: %s", resp.StatusCode, raw)
	}

	var r struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	var text string
	for _, o := range r.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			if c.Type == "output_text" {
				text += c.Text
			}
		}
	}
	if text == "" {
		return nil, fmt.Errorf("openai: empty output (status %s)", r.Status)
	}
	var out struct {
		Codes []Found `json:"codes"`
	}
	if err := json.Unmarshal([]byte(jsonObject(text)), &out); err != nil {
		return nil, fmt.Errorf("openai: bad json: %w", err)
	}
	return out.Codes, nil
}

// jsonObject trims anything around the outermost {...}: some models behind
// compatible APIs wrap structured output in prose or code fences.
func jsonObject(s string) string {
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}
