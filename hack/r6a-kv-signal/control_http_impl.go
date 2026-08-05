package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	reader := http.MaxBytesReader(writer, request.Body, maxControlBody)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("解析请求 JSON: %w", err))
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}
