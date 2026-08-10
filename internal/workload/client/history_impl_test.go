package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryRoundTripAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history.json")
	history := []Message{{Role: RoleSystem, Content: "be concise"}, {Role: RoleUser, Content: "hello"}}
	if err := SaveHistory(path, history); err != nil {
		t.Fatalf("save history: %v", err)
	}
	loaded, err := LoadHistory(path)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(loaded) != len(history) || loaded[1] != history[1] {
		t.Fatalf("loaded history = %+v", loaded)
	}
	if err := ClearHistory(path); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("history still exists after clear: %v", err)
	}
}

func TestHistoryRejectsInvalidMessagesAndMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := SaveHistory(path, []Message{{Role: "tool", Content: "unsupported"}}); err == nil {
		t.Fatal("unsupported role was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"messages":[{"role":"user","content":"ok"}],"api_key":"leak"}`), 0o600); err != nil {
		t.Fatalf("write malformed history: %v", err)
	}
	if _, err := LoadHistory(path); err == nil {
		t.Fatal("history with unexpected fields was accepted")
	}
}
