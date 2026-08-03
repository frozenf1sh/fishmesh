package gateway

const (
	httpScheme  = "http"
	httpsScheme = "https"

	healthRoute  = "GET /healthz"
	readyRoute   = "GET /readyz"
	metricsRoute = "GET /metrics"
	proxyRoute   = "/v1/"

	headerRequestID          = "X-Request-ID"
	headerRetryAfter         = "Retry-After"
	headerRouteReason        = "X-FishMesh-Route-Reason"
	headerPrefixKey          = "X-FishMesh-Prefix-Key"
	headerRoutingMode        = "X-FishMesh-Routing-Mode"
	headerBackendID          = "X-FishMesh-Backend-ID"
	headerPreferredBackendID = "X-FishMesh-Preferred-Backend-ID"
	headerPolicy             = "X-FishMesh-Policy"
	headerSpilloverReason    = "X-FishMesh-Spillover-Reason"
	headerUpstream           = "X-FishMesh-Upstream"
	headerConnection         = "Connection"
	headerKeepAlive          = "Keep-Alive"
	headerProxyAuthenticate  = "Proxy-Authenticate"
	headerProxyAuthorization = "Proxy-Authorization"
	headerTE                 = "Te"
	headerTrailer            = "Trailer"
	headerTransferEncoding   = "Transfer-Encoding"
	headerUpgrade            = "Upgrade"

	connectionClose    = "close"
	retryAfterSeconds  = "1"
	serviceBackendID   = "service"
	statusClientClosed = 499
	streamBufferBytes  = 32 * 1024
	requestIDBytes     = 16
	sseDataPrefix      = "data:"
	sseTerminalData    = "[DONE]"
)
