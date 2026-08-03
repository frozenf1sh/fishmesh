package gateway

import "net/http"

// Handler 返回 health、readiness、metrics 和 OpenAI `/v1/` 请求入口。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(healthRoute, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(readyRoute, func(writer http.ResponseWriter, _ *http.Request) {
		if !s.requestPath.Ready() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(metricsRoute, func(writer http.ResponseWriter, request *http.Request) {
		s.metrics.updateRequestPath(s.requestPath.State(request.Context()))
		s.metrics.handler().ServeHTTP(writer, request)
	})
	mux.HandleFunc(proxyRoute, s.proxy)
	return mux
}
