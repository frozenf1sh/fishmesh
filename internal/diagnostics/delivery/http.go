package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/application"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

// HTTPServer 暴露只读诊断 API。分析请求同步执行，便于 MVP 验证；后续长耗时
// 任务可以在同一 Engine 之上增加异步 job store，而不改变领域模型。
type HTTPServer struct {
	engine         *application.Engine
	descriptors    []domain.ToolDescriptor
	maxBody        int64
	requestTimeout time.Duration
}

func NewHTTPServer(engine *application.Engine, descriptors []domain.ToolDescriptor, requestTimeout time.Duration) (*HTTPServer, error) {
	if engine == nil {
		return nil, errors.New("engine is required")
	}
	if requestTimeout <= 0 {
		requestTimeout = 5 * time.Second
	}
	return &HTTPServer{engine: engine, descriptors: append([]domain.ToolDescriptor(nil), descriptors...), maxBody: 1 << 20, requestTimeout: requestTimeout}, nil
}

func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /v1/tools", s.tools)
	mux.HandleFunc("POST /v1/analyze", s.analyze)
	return mux
}

func (s *HTTPServer) tools(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"planner": "rules-v1", "tools": s.descriptors})
}

func (s *HTTPServer) analyze(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, s.maxBody)
	var incident domain.Incident
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&incident); err != nil && !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid incident JSON")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "incident body must contain one JSON object")
		return
	}
	if err := request.Body.Close(); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid incident body")
		return
	}
	ctx := request.Context()
	if deadline, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.requestTimeout)
		defer cancel()
	} else if time.Until(deadline) <= 0 {
		writeError(writer, http.StatusRequestTimeout, "analysis deadline exceeded")
		return
	}
	report := s.engine.Analyze(ctx, incident)
	writeJSON(writer, http.StatusOK, report)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}
