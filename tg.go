package main

// Minimal Telegram Bot API client: only what the bot needs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type TG struct {
	base string
	hc   *http.Client
}

func NewTG(token string) *TG {
	return &TG{
		base: "https://api.telegram.org/bot" + token + "/",
		hc:   &http.Client{Timeout: 70 * time.Second},
	}
}

type APIError struct {
	Code       int
	Desc       string
	RetryAfter int // seconds, for 429
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram %d: %s", e.Code, e.Desc) }

func isBlocked(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == 403
}

type TUser struct {
	ID           int64  `json:"id"`
	LanguageCode string `json:"language_code"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	From      *TUser `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    TUser    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type CopyText struct {
	Text string `json:"text"`
}

type Button struct {
	Text         string    `json:"text"`
	CallbackData string    `json:"callback_data,omitempty"`
	CopyText     *CopyText `json:"copy_text,omitempty"`
}

type Keyboard struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}

// call retries on 429 (Telegram flood control) using the advised delay.
func (t *TG) call(ctx context.Context, method string, req, out any) error {
	for attempt := 0; ; attempt++ {
		err := t.call1(ctx, method, req, out)
		var ae *APIError
		if !errors.As(err, &ae) || ae.Code != 429 || attempt == 3 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(ae.RetryAfter+1) * time.Second):
		}
	}
}

func (t *TG) call1(ctx context.Context, method string, req, out any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(r)
	if err != nil {
		var ue *url.Error // strip URL so the token never reaches logs
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if !env.OK {
		return &APIError{env.ErrorCode, env.Description, env.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (t *TG) Updates(ctx context.Context, offset int) ([]Update, error) {
	var ups []Update
	err := t.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         50,
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

func (t *TG) Send(ctx context.Context, chatID int64, text string, kb *Keyboard) error {
	req := map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if kb != nil {
		req["reply_markup"] = kb
	}
	return t.call(ctx, "sendMessage", req, nil)
}

// EditText replaces a message's text; kb nil removes the inline keyboard.
func (t *TG) EditText(ctx context.Context, chatID int64, msgID int, text string, kb *Keyboard) error {
	req := map[string]any{
		"chat_id": chatID, "message_id": msgID, "text": text, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if kb != nil {
		req["reply_markup"] = kb
	}
	return t.call(ctx, "editMessageText", req, nil)
}

func (t *TG) Answer(ctx context.Context, id, text string) error {
	return t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

type Cmd struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetCommands sets the command menu. lang "" = fallback for all languages;
// chatID 0 = default scope, otherwise only that chat (overrides the default).
func (t *TG) SetCommands(ctx context.Context, cmds []Cmd, lang string, chatID int64) error {
	req := map[string]any{"commands": cmds}
	if lang != "" {
		req["language_code"] = lang
	}
	if chatID != 0 {
		req["scope"] = map[string]any{"type": "chat", "chat_id": chatID}
	}
	return t.call(ctx, "setMyCommands", req, nil)
}
