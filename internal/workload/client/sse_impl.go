package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	sseDataPrefix = "data:"
	sseDone       = "[DONE]"
	maxSSELine    = 1 << 20
)

type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func consumeStream(reader io.Reader, startedAt time.Time, output io.Writer) (string, time.Duration, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELine)
	var text strings.Builder
	var firstEventAt time.Time
	for scanner.Scan() {
		data, isData := sseData(scanner.Text())
		if !isData {
			continue
		}
		if data == sseDone {
			if firstEventAt.IsZero() {
				return "", 0, fmt.Errorf("stream ended before first SSE event")
			}
			return text.String(), firstEventAt.Sub(startedAt), nil
		}
		if firstEventAt.IsZero() {
			firstEventAt = time.Now()
		}
		content, err := streamContent(data)
		if err != nil {
			return text.String(), 0, err
		}
		text.WriteString(content)
		if output != nil && content != "" {
			if _, err := io.WriteString(output, content); err != nil {
				return text.String(), 0, fmt.Errorf("write stream output: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return text.String(), 0, fmt.Errorf("read SSE stream: %w", err)
	}
	return text.String(), 0, fmt.Errorf("stream ended before terminal SSE event")
}

func sseData(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, sseDataPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, sseDataPrefix)), true
}

func streamContent(data string) (string, error) {
	var response streamResponse
	if err := json.Unmarshal([]byte(data), &response); err != nil {
		return "", fmt.Errorf("decode SSE event: %w", err)
	}
	var text strings.Builder
	for _, choice := range response.Choices {
		text.WriteString(choice.Delta.Content)
	}
	return text.String(), nil
}
