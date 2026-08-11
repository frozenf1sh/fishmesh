package gateway

import "net/http"

// Handler 返回 Gateway 的 HTTP 路由表。
//
// 路由表只包含四类入口：/healthz 表示进程仍能响应，/readyz 表示 requestpath
// 当前是否拥有可用的 discovery 状态，/metrics 投影内部低基数指标，/v1/ 负责
// OpenAI-compatible 请求。健康检查不访问上游；就绪检查也不主动探测模型，而是
// 复用 requestpath 已经维护的 membership/freshness 判断，避免探针本身制造推理流量。
//
// 每次调用都会创建一个新的 ServeMux，因此 Handler 可以安全地用于测试或由上层
// 挂载到不同的 http.Server；Server 本身的 admission、transport 和 requestpath
// 状态仍然由注入的依赖共享。
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
