package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const historyFileMode = 0o600

type historyDocument struct {
	Messages []Message `json:"messages"`
}

// LoadHistory reads a user-selected local conversation history. A missing file is an empty conversation.
func LoadHistory(path string) ([]Message, error) {
	if path == "" {
		return nil, fmt.Errorf("history path must not be empty")
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	var document historyDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode history: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("decode history: multiple JSON documents")
	}
	if err := validateMessages(document.Messages); err != nil {
		return nil, fmt.Errorf("validate history: %w", err)
	}
	return append([]Message(nil), document.Messages...), nil
}

// SaveHistory atomically replaces only the explicit local history path after validating its privacy-safe schema.
func SaveHistory(path string, messages []Message) error {
	if path == "" {
		return fmt.Errorf("history path must not be empty")
	}
	if err := validateMessages(messages); err != nil {
		return err
	}
	body, err := json.MarshalIndent(historyDocument{Messages: messages}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode history: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".fishmesh-history-*")
	if err != nil {
		return fmt.Errorf("create temporary history: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(historyFileMode); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary history: %w", err)
	}
	if _, err := temporary.Write(append(body, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary history: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary history: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace history atomically: %w", err)
	}
	return nil
}

// ClearHistory removes only the explicit history file; a missing file is already clear.
func ClearHistory(path string) error {
	if path == "" {
		return fmt.Errorf("history path must not be empty")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear history: %w", err)
	}
	return nil
}

func validateMessages(messages []Message) error {
	for _, message := range messages {
		switch message.Role {
		case RoleSystem, RoleUser, RoleAssistant:
		default:
			return fmt.Errorf("unsupported message role %q", message.Role)
		}
		if message.Content == "" {
			return fmt.Errorf("message content must not be empty")
		}
	}
	return nil
}
