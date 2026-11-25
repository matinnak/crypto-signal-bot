package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample() *State {
	st := New()
	st.UpdateOffset = 42
	st.Strategies["rsi"] = &Strategy{Name: "rsi", Buy: "RSI(14) < 30"}
	st.Strategies["ema_cross"] = &Strategy{Name: "ema_cross", Buy: "crossover(EMA(9), EMA(21))"}
	st.Users[123] = &User{ChatID: 123, Approved: true, Subs: []*Subscription{{Symbol: "BTCUSDT", Interval: "1h", Strategy: "rsi"}}}
	return st
}

func TestRoundTripPlain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Save(path, "", sample()); err != nil {
		t.Fatal(err)
	}
	st, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.UpdateOffset != 42 || st.Users[123].Subs[0].Symbol != "BTCUSDT" {
		t.Fatalf("bad round trip: %+v", st)
	}
}

func TestRoundTripEncrypted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.enc")
	if err := Save(path, "secret", sample()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "BTCUSDT") {
		t.Fatal("state is not encrypted")
	}
	if _, err := Load(path, "wrong"); err == nil {
		t.Fatal("expected error with wrong key")
	}
	if _, err := Load(path, ""); err == nil {
		t.Fatal("expected error without key")
	}
	st, err := Load(path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Strategies) != 2 {
		t.Fatalf("expected 2 strategies, got %d", len(st.Strategies))
	}
}

func TestMissingFile(t *testing.T) {
	st, err := Load(filepath.Join(t.TempDir(), "nope"), "k")
	if err != nil || len(st.Users) != 0 {
		t.Fatalf("expected empty state, got %v %v", st, err)
	}
}

func TestFindStrategy(t *testing.T) {
	st := sample()
	// sorted: ema_cross (1), rsi (2)
	if s, ok := st.FindStrategy("2"); !ok || s.Name != "rsi" {
		t.Errorf("by number: %v %v", s, ok)
	}
	if s, ok := st.FindStrategy("EMA_CROSS"); !ok || s.Name != "ema_cross" {
		t.Errorf("by name: %v %v", s, ok)
	}
	if _, ok := st.FindStrategy("3"); ok {
		t.Error("out of range number should fail")
	}
	if st.StrategyUsers("rsi") != 1 {
		t.Error("StrategyUsers")
	}
}
