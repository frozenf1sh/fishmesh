package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kubernetes "github.com/frozenf1sh/fishmesh/internal/platform/kubernetes"
	"github.com/frozenf1sh/fishmesh/internal/serving/endpoint"
	"github.com/frozenf1sh/fishmesh/internal/serving/identity"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
	"github.com/frozenf1sh/fishmesh/internal/serving/transport"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server is a transparent OpenAI-compatible proxy. Routing, endpoint discovery,
// transport reuse and metrics are separate boundaries so future EndpointSlice,
// vLLM-metrics and hybrid policies do not require rewriting the proxy.
type Server struct {
	config       Config
	service      routing.Backend
	resolver     endpoint.Resolver
	strategy     routing.Strategy
	backendURLs  map[string]*url.URL
	pool         *transport.Pool
	observations *observation.Service
	inflight     map[string]*atomic.Int64
	inflightMu   sync.RWMutex
	metrics      *metrics
	logger       *slog.Logger
	registry     *prometheus.Registry
}

type routeDecision struct {
	backend  routing.Backend
	reason   string
	upstream *url.URL
	client   *http.Client
	release  func()
}

func NewServer(config Config, logger *slog.Logger) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	upstream, err := url.Parse(config.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	service := routing.Backend{ID: "service", URL: upstream.String()}
	strategy, err := routing.New(config.RoutingMode, service)
	if err != nil {
		return nil, err
	}
	backendURLs := map[string]*url.URL{service.ID: upstream}
	backends := make([]routing.Backend, 0, len(config.BackendEndpoints))
	for index, rawEndpoint := range config.BackendEndpoints {
		parsed, parseErr := url.Parse(rawEndpoint)
		if parseErr != nil {
			return nil, fmt.Errorf("parse backend endpoint: %w", parseErr)
		}
		backend := routing.Backend{ID: fmt.Sprintf("backend-%d", index), URL: parsed.String()}
		backends = append(backends, backend)
		backendURLs[backend.ID] = parsed
	}
	var resolver endpoint.Resolver
	var kubernetesClient *http.Client
	if config.EndpointDiscovery == "endpointslice" {
		kubernetesClient = http.DefaultClient
		if config.KubernetesCA != "" {
			kubernetesClient, err = kubernetes.NewHTTPClient(kubernetesClient, config.KubernetesCA)
			if err != nil {
				return nil, err
			}
		}
		resolver, err = endpoint.NewEndpointSlice(endpoint.EndpointSliceConfig{
			Namespace: config.EndpointNamespace, ServiceName: config.EndpointService,
			BaseURL: config.KubernetesAPIURL, TokenFile: config.KubernetesToken,
			CAFile: config.KubernetesCA, HTTPClient: kubernetesClient, RefreshInterval: config.EndpointRefresh,
		})
	} else {
		resolverBackends := backends
		if len(resolverBackends) == 0 {
			resolverBackends = []routing.Backend{service}
		}
		resolver, err = endpoint.NewStatic(resolverBackends)
	}
	if err != nil {
		return nil, err
	}
	var observations *observation.Service
	if config.ObservationMode == "prometheus" {
		var identityProvider observation.IdentityProvider
		if config.EndpointDiscovery == "endpointslice" {
			identityProvider, err = identity.New(identity.Config{
				Namespace: config.EndpointNamespace, BaseURL: config.KubernetesAPIURL,
				TokenFile: config.KubernetesToken, CAFile: config.KubernetesCA,
				HTTPClient: kubernetesClient,
			})
			if err != nil {
				_ = resolver.Close()
				return nil, err
			}
		}
		observations, err = observation.New(observation.Config{
			Resolver:  resolver,
			Collector: observation.PrometheusCollector{HTTPClient: http.DefaultClient},
			Identity:  identityProvider,
			Interval:  config.ObservationInterval,
			MaxAge:    config.ObservationMaxAge,
		})
		if err != nil {
			_ = resolver.Close()
			return nil, err
		}
	}

	inflight := make(map[string]*atomic.Int64, len(backends)+1)
	inflight[service.ID] = &atomic.Int64{}
	for _, backend := range backends {
		inflight[backend.ID] = &atomic.Int64{}
	}
	registry := prometheus.NewRegistry()
	return &Server{
		config: config, service: service, resolver: resolver, strategy: strategy,
		backendURLs:  backendURLs,
		pool:         transport.NewPool(transport.Config{KeepAlive: config.KeepAlive, RequestTimeout: config.RequestTimeout}),
		observations: observations,
		inflight:     inflight, metrics: newMetrics(registry), logger: logger,
		registry: registry,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, _ *http.Request) {
		if !s.ready() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /metrics", func(writer http.ResponseWriter, request *http.Request) {
		if s.observations != nil {
			s.metrics.UpdateBackendObservations(s.observations.Snapshot())
		}
		s.metrics.UpdateEndpointDiscovery(s.resolver.Status())
		promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}).ServeHTTP(writer, request)
	})
	mux.HandleFunc("/v1/", s.proxy)
	return mux
}

// Close releases gateway-owned transport resources. In-flight requests are
// still governed by http.Server.Shutdown; this method only closes idle pools.
func (s *Server) Close() {
	if s.observations != nil {
		_ = s.observations.Close()
	}
	if s.resolver != nil {
		_ = s.resolver.Close()
	}
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *Server) proxy(writer http.ResponseWriter, request *http.Request) {
	startedAt := time.Now()
	s.metrics.inflight.Inc()
	defer s.metrics.inflight.Dec()
	status := http.StatusBadGateway
	defer func() {
		statusLabel := strconv.Itoa(status)
		s.metrics.requests.WithLabelValues(request.Method, statusLabel).Inc()
		s.metrics.requestSeconds.WithLabelValues(request.Method, statusLabel).Observe(time.Since(startedAt).Seconds())
	}()

	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = newRequestID()
	}
	decision := s.route(request.Context(), request.Header.Get("X-FishMesh-Prefix-Key"))
	defer decision.release()
	s.metrics.routingDecisions.WithLabelValues(s.config.RoutingMode, decision.backend.ID).Inc()

	upstreamRequest, cancel, err := s.newUpstreamRequest(request, requestID, decision.upstream)
	if err != nil {
		http.Error(writer, "invalid upstream request", http.StatusBadRequest)
		status = http.StatusBadRequest
		return
	}
	defer cancel()
	response, err := decision.client.Do(upstreamRequest)
	if err != nil {
		s.metrics.upstreamErrors.Inc()
		s.logger.Warn("upstream request failed", "request_id", requestID, "backend_id", decision.backend.ID, "error", err)
		http.Error(writer, "inference upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	copyResponseHeaders(writer.Header(), response.Header)
	writer.Header().Set("X-Request-ID", requestID)
	writer.Header().Set("X-FishMesh-Routing-Mode", s.config.RoutingMode)
	writer.Header().Set("X-FishMesh-Route-Reason", decision.reason)
	writer.Header().Set("X-FishMesh-Backend-ID", decision.backend.ID)
	writer.Header().Set("X-FishMesh-Upstream", decision.upstream.Host)
	status = response.StatusCode
	writer.WriteHeader(status)

	detector := &sseDetector{}
	firstEventRecorded := false
	copyResponseBody(writer, response.Body, func() {
		if !firstEventRecorded {
			firstEventRecorded = true
			s.metrics.ttftSeconds.Observe(time.Since(startedAt).Seconds())
		}
	}, detector)
}

func (s *Server) route(ctx context.Context, prefixKey string) routeDecision {
	backends, err := s.resolver.Snapshot(ctx)
	if err != nil {
		backends = nil
	}
	snapshot := routing.Snapshot{Backends: backends, Inflight: make(map[string]int64)}
	if s.observations != nil {
		snapshot.Observations = s.observations.Snapshot()
		s.metrics.UpdateBackendObservations(snapshot.Observations)
	}
	s.metrics.UpdateEndpointDiscovery(s.resolver.Status())
	s.inflightMu.RLock()
	for id, value := range s.inflight {
		snapshot.Inflight[id] = value.Load()
	}
	s.inflightMu.RUnlock()
	decision, err := s.strategy.Select(prefixKey, snapshot)
	if err != nil || decision.Backend.ID == "" {
		decision = routing.Decision{Backend: s.service, Reason: "strategy-fallback"}
		s.metrics.routeFallbacks.Inc()
	}
	upstream := s.backendURLs[decision.Backend.ID]
	if upstream == nil && decision.Backend.URL != "" {
		upstream, err = url.Parse(decision.Backend.URL)
		if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" {
			upstream = nil
		}
	}
	if upstream == nil {
		decision = routing.Decision{Backend: s.service, Reason: "backend-fallback"}
		upstream = s.backendURLs[s.service.ID]
		s.metrics.routeFallbacks.Inc()
	}
	state := s.inflightCounter(decision.Backend.ID)
	state.Add(1)
	backend := decision.Backend
	return routeDecision{
		backend: backend, reason: decision.Reason, upstream: upstream, client: s.pool.ClientFor(backend),
		release: func() { state.Add(-1) },
	}
}

func (s *Server) ready() bool {
	status := s.resolver.Status()
	if status.Status == routing.ObservationUnavailable {
		return false
	}
	if s.config.EndpointDiscovery == "endpointslice" && status.Freshness > s.config.EndpointMaxAge {
		return false
	}
	return true
}

func (s *Server) inflightCounter(backendID string) *atomic.Int64 {
	s.inflightMu.RLock()
	counter := s.inflight[backendID]
	s.inflightMu.RUnlock()
	if counter != nil {
		return counter
	}
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if counter = s.inflight[backendID]; counter == nil {
		counter = &atomic.Int64{}
		s.inflight[backendID] = counter
	}
	return counter
}

func (s *Server) newUpstreamRequest(request *http.Request, requestID string, upstream *url.URL) (*http.Request, context.CancelFunc, error) {
	contextWithTimeout, cancel := context.WithTimeout(request.Context(), s.config.RequestTimeout)
	upstreamRequest := request.Clone(contextWithTimeout)
	upstreamRequest.URL.Scheme = upstream.Scheme
	upstreamRequest.URL.Host = upstream.Host
	upstreamRequest.URL.Path = joinURLPath(upstream.Path, request.URL.Path)
	upstreamRequest.URL.RawPath = ""
	upstreamRequest.Host = upstream.Host
	upstreamRequest.RequestURI = ""
	removeHopByHopHeaders(upstreamRequest.Header)
	upstreamRequest.Header.Set("X-Request-ID", requestID)
	if !s.config.KeepAlive {
		upstreamRequest.Header.Set("Connection", "close")
	}
	return upstreamRequest, cancel, nil
}

func joinURLPath(basePath, requestPath string) string {
	if basePath == "" || basePath == "/" {
		return requestPath
	}
	return path.Join(basePath, requestPath)
}

func copyResponseBody(writer http.ResponseWriter, body io.Reader, onFirstEvent func(), detector *sseDetector) {
	bufferedWriter := bufio.NewWriterSize(writer, 32*1024)
	defer bufferedWriter.Flush()
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := body.Read(buffer)
		if read > 0 {
			if detector.Feed(buffer[:read]) {
				onFirstEvent()
			}
			if _, writeErr := bufferedWriter.Write(buffer[:read]); writeErr != nil {
				return
			}
			if flusher, ok := writer.(http.Flusher); ok {
				bufferedWriter.Flush()
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

func copyResponseHeaders(destination, source http.Header) {
	cleaned := source.Clone()
	removeHopByHopHeaders(cleaned)
	for key, values := range cleaned {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func removeHopByHopHeaders(headers http.Header) {
	for _, value := range headers.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			headers.Del(strings.TrimSpace(token))
		}
	}
	for _, header := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		headers.Del(header)
	}
}

func newRequestID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(bytes)
}

type sseDetector struct {
	line    []byte
	emitted bool
}

func (d *sseDetector) Feed(chunk []byte) bool {
	if d.emitted {
		return false
	}
	for _, character := range chunk {
		if character != '\n' {
			d.line = append(d.line, character)
			continue
		}
		line := strings.TrimSpace(string(d.line))
		d.line = d.line[:0]
		if strings.HasPrefix(line, "data:") && strings.TrimSpace(strings.TrimPrefix(line, "data:")) != "[DONE]" {
			d.emitted = true
			return true
		}
	}
	return false
}
