package simulator

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Handler 返回数据面、指标和测试控制面路由。
func (b *Backend) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(healthRoute, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(modelsRoute, b.serveModels)
	mux.HandleFunc(completionsRoute, b.serveCompletion)
	mux.HandleFunc(metricsRoute, b.serveMetrics)
	mux.HandleFunc(controlRoute, b.updateBehavior)
	mux.HandleFunc(stateRoute, b.serveState)
	return mux
}

func (b *Backend) serveModels(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set(headerContentType, mediaTypeJSON)
	_, _ = writer.Write([]byte(modelsResponse))
}

func (b *Backend) serveCompletion(writer http.ResponseWriter, request *http.Request) {
	behavior := b.Snapshot().Behavior
	b.requests.Add(1)
	b.active.Add(1)
	canceled := false
	defer func() { b.finishRequest(request, canceled) }()

	if behavior.StatusCode >= http.StatusBadRequest {
		b.forcedErrors.Add(1)
		http.Error(writer, "simulated upstream failure", behavior.StatusCode)
		return
	}

	writer.Header().Set(headerContentType, mediaTypeSSE)
	writer.WriteHeader(behavior.StatusCode)
	if behavior.Hold {
		_, _ = fmt.Fprintf(writer, sseEventFormat, 1, 1)
		flush(writer)
		canceled = holdStream(writer, request.Context())
		return
	}
	flush(writer)
	if !wait(request.Context(), behavior.FirstEventDelay) {
		return
	}
	b.writeEvents(writer, request, behavior)
}

func (b *Backend) writeEvents(writer http.ResponseWriter, request *http.Request, behavior Behavior) {
	for event := 1; event <= behavior.Events; event++ {
		if _, err := fmt.Fprintf(writer, sseEventFormat, event, event); err != nil {
			return
		}
		flush(writer)
		if behavior.AbortAfterEvents == event {
			b.streamAborts.Add(1)
			panic(http.ErrAbortHandler)
		}
		if event < behavior.Events && !wait(request.Context(), behavior.EventInterval) {
			return
		}
	}
	_, _ = writer.Write([]byte(sseDone))
	flush(writer)
}

func (b *Backend) finishRequest(request *http.Request, canceled bool) {
	b.active.Add(-1)
	if canceled || request.Context().Err() != nil {
		b.cancellations.Add(1)
	}
}

func holdStream(writer http.ResponseWriter, ctx context.Context) bool {
	ticker := time.NewTicker(holdHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return true
		case <-ticker.C:
			if _, err := writer.Write([]byte(sseHeartbeat)); err != nil {
				return true
			}
			flush(writer)
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	if duration == 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func flush(writer http.ResponseWriter) {
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
