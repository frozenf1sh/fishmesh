package simulator

import "time"

const (
	healthRoute      = "GET /healthz"
	modelsRoute      = "GET /v1/models"
	completionsRoute = "POST /v1/chat/completions"
	metricsRoute     = "GET /metrics"
	controlRoute     = "PUT /control/behavior"
	stateRoute       = "GET /control/state"

	headerContentType = "Content-Type"
	mediaTypeJSON     = "application/json"
	mediaTypeSSE      = "text/event-stream"
	mediaTypeText     = "text/plain; version=0.0.4"

	metricRequestsWaiting = "vllm:num_requests_waiting"
	metricRequestsRunning = "vllm:num_requests_running"

	sseEventFormat        = "data: {\"id\":\"simulated-%d\",\"choices\":[{\"delta\":{\"content\":\"token-%d\"}}]}\n\n"
	sseDone               = "data: [DONE]\n\n"
	sseHeartbeat          = ": fishmesh-simulator-hold\n\n"
	modelsResponse        = `{"object":"list","data":[{"id":"fishmesh-simulator","object":"model"}]}`
	maxControlBody        = 64 << 10
	holdHeartbeatInterval = 10 * time.Millisecond
)
