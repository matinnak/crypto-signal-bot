// Package telegram is a minimal Telegram Bot API client (getUpdates and
// sendMessage) built on the standard library only.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxMessageLen is Telegram's limit for a single text message.
const MaxMessageLen = 4096

// Client talks to the Bot API.
type Client struct {
	Token   string
	BaseURL string // default https://api.telegram.org
	HTTP    *http.Client
}

// New returns a client for the given bot token.
func New(token string) *Client {
	return &Client{
		Token:   token,
		BaseURL: "https://api.telegram.org",
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// User is the sender of a message.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// DisplayName returns "First Last (@username)" with empty parts omitted.
func (u *User) DisplayName() string {
	if u == nil {
		return ""
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		if name != "" {
			name += " "
		}
		name += "@" + u.Username
	}
	return name
}

// Chat is where a message was sent.
type Chat struct {
	ID int64 `json:"id"`
}

// Message is an incoming message.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	Caption   string `json:"caption"`
}

// Update is one item returned by getUpdates.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// APIError is an error reported by Telegram.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

func (c *Client) call(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/%s", c.BaseURL, c.Token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Never include the URL in the error: it contains the bot token.
		return fmt.Errorf("telegram %s: request failed: %w", method, redact(err, c.Token))
	}
	defer resp.Body.Close()

	var r apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: decode (HTTP %d): %w", method, resp.StatusCode, err)
	}
	if !r.OK {
		e := &APIError{Method: method, Code: r.ErrorCode, Description: r.Description}
		if r.Parameters != nil {
			e.RetryAfter = time.Duration(r.Parameters.RetryAfter) * time.Second
		}
		return e
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// redact removes the token from an error message (url.Error includes the URL).
func redact(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "<token>"))
}

// GetUpdates returns pending updates starting at offset. timeout is the long
// polling time; zero returns immediately.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	payload := map[string]any{
		"offset":          offset,
		"timeout":         int(timeout / time.Second),
		"allowed_updates": []string{"message"},
	}
	var ups []Update
	err := c.call(ctx, "getUpdates", payload, &ups)
	return ups, err
}

// SendMessage sends plain text, splitting it into several messages if it is
// longer than Telegram allows, and retrying once when rate-limited.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	for _, part := range Split(text, MaxMessageLen) {
		payload := map[string]any{
			"chat_id":                  chatID,
			"text":                     part,
			"disable_web_page_preview": true,
		}
		err := c.call(ctx, "sendMessage", payload, nil)
		var apiErr *APIError
		if err != nil && errors.As(err, &apiErr) && apiErr.RetryAfter > 0 && apiErr.RetryAfter <= time.Minute {
			select {
			case <-time.After(apiErr.RetryAfter):
			case <-ctx.Done():
				return ctx.Err()
			}
			err = c.call(ctx, "sendMessage", payload, nil)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Split breaks text into chunks of at most limit characters, preferring to
// cut at line breaks.
func Split(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var parts []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if cur.Len() > 0 {
			parts = append(parts, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
			curLen = 0
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		n := utf8.RuneCountInString(line)
		if curLen+n > limit {
			flush()
		}
		for n > limit { // a single very long line
			r := []rune(line)
			parts = append(parts, string(r[:limit]))
			line = string(r[limit:])
			n -= limit
		}
		cur.WriteString(line)
		curLen += n
	}
	flush()
	return parts
}
