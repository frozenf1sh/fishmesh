package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVLLMClientRenderAndGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case renderPath:
			_, _ = writer.Write([]byte(`{"token_ids":[1,2,3]}`))
		case generatePath:
			_, _ = writer.Write([]byte(`{"id":"completion-1"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := newVLLMClient()
	tokens, _, err := client.Render(context.Background(), server.URL, json.RawMessage(`{"model":"model-a"}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(tokens) != 3 || tokens[2] != 3 {
		t.Fatalf("token_ids = %v", tokens)
	}

	status, body, _, err := client.Generate(context.Background(), server.URL, json.RawMessage(`{"model":"model-a"}`))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"id":"completion-1"}` {
		t.Fatalf("Generate status=%d body=%s", status, body)
	}
}

func TestRenderedModel(t *testing.T) {
	if model := renderedModel(json.RawMessage(`{"model":"model-a"}`)); model != "model-a" {
		t.Fatalf("model = %q", model)
	}
	if model := renderedModel(json.RawMessage(`{}`)); model != defaultModelName {
		t.Fatalf("default model = %q", model)
	}
}
