package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplit(t *testing.T) {
	if got := Split("short", 10); len(got) != 1 {
		t.Fatalf("short text split: %v", got)
	}
	text := strings.Repeat("سلام دنیا\n", 50) // 500 runes
	parts := Split(text, 100)
	if len(parts) < 5 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	for _, p := range parts {
		if utf8.RuneCountInString(p) > 100 {
			t.Errorf("part too long: %d", utf8.RuneCountInString(p))
		}
	}
	long := strings.Repeat("x", 250)
	for _, p := range Split(long, 100) {
		if len(p) > 100 {
			t.Errorf("long line part too long: %d", len(p))
		}
	}
}

func TestSendAndGetUpdates(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/botTOKEN/") {
			t.Errorf("bad path %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			w.Write([]byte(`{"ok":true,"result":[{"update_id":7,"message":{"chat":{"id":1},"text":"/start"}}]}`))
		case got["chat_id"] == float64(2):
			w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
		default:
			w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	defer srv.Close()

	c := New("TOKEN")
	c.BaseURL = srv.URL
	ups, err := c.GetUpdates(context.Background(), 5, 0)
	if err != nil || len(ups) != 1 || ups[0].Message.Text != "/start" {
		t.Fatalf("getUpdates: %v %+v", err, ups)
	}
	if err := c.SendMessage(context.Background(), 1, "hi"); err != nil {
		t.Fatal(err)
	}
	if got["text"] != "hi" {
		t.Errorf("payload: %v", got)
	}
	err = c.SendMessage(context.Background(), 2, "hi")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 error, got %v", err)
	}
}
