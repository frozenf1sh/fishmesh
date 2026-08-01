package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	config          Config
	service         routing.Backend
	resolver        endpoint.Resolver
	strategy        routing.Strategy
	backendURLs     map[string]*url.URL
	pool            *transport.Pool
	observations    *observation.Service
	inflight        map[string]*atomic.Int64
	inflightMu      sync.RWMutex
	metrics         *metrics
	logger          *slog.Logger
	registry        *prometheus.Registry
	circuits        *routing.CircuitRegistry
	admission       chan struct{}
	active          map[string]struct{}
	lifecycleCancel context.CancelFunc
	lifecycleDone   chan struct{}
	close           sync.Once
}

type routeDecision struct {
	backend            routing.Backend
	preferredBackendID string
	reason             string
	spilloverReason    string
	policy             string
	upstream           *url.URL
	client             *http.Client
	release            func()
}

func NewServer(config Config, logger *slog.Logger) (*Server, error) {
	config = config.withReliabilityDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	upstream, err := url.Parse(config.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	service := routing.Backend{ID: "service", URL: upstream.String()}
	var strategy routing.Strategy
	if config.RoutingMode == routing.ModeBoundedAffinity {
		strategy, err = routing.NewBoundedAffinity(routing.BoundedAffinityConfig{
			TTL: config.AffinityTTL, MaxEntries: config.AffinityMaxEntries,
			InflightDelta:   config.AffinityInflightDelta,
			QueueDepthDelta: config.AffinityQueueDepthDelta,
		})
	} else {
		strategy, err = routing.New(config.RoutingMode, service)
	}
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
	circuits, err := routing.NewCircuitRegistry(routing.CircuitConfig{
		EWMAAlpha: config.CircuitEWMAAlpha, ErrorThreshold: config.CircuitErrorThreshold,
		MinimumRequests: config.CircuitMinimumRequests, OpenDuration: config.CircuitOpenDuration,
	})
	if err != nil {
		_ = resolver.Close()
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
	server := &Server{
		config: config, service: service, resolver: resolver, strategy: strategy,
		backendURLs:  backendURLs,
		pool:         transport.NewPool(transport.Config{KeepAlive: config.KeepAlive, RequestTimeout: config.RequestTimeout, MaxConnsPerHost: config.MaxConnsPerHost}),
		observations: observations,
		inflight:     inflight, metrics: newMetrics(registry), logger: logger,
		registry: registry, circuits: circuits, admission: make(chan struct{}, config.MaxInflightRequests),
		active: map[string]struct{}{service.ID: {}}, lifecycleDone: make(chan struct{}),
	}
	server.startLifecycle()
	return server, nil
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
			states := s.observations.Snapshot()
			if backends, err := s.resolver.Snapshot(request.Context()); err == nil {
				s.reconcileBackendState(backends)
				states = observationsForBackends(states, backends)
			}
			s.metrics.UpdateBackendObservations(states)
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
	s.close.Do(func() {
		if s.lifecycleCancel != nil {
			s.lifecycleCancel()
			<-s.lifecycleDone
		}
		if s.observations != nil {
			_ = s.observations.Close()
		}
		if s.resolver != nil {
			_ = s.resolver.Close()
		}
		if s.pool != nil {
			s.pool.Close()
		}
	})
}

func (s *Server) startLifecycle() {
	if s.config.EndpointDiscovery != "endpointslice" {
		close(s.lifecycleDone)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.lifecycleCancel = cancel
	go func() {
		defer close(s.lifecycleDone)
		s.reconcileFromResolver(ctx)
		ticker := time.NewTicker(s.config.EndpointRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.reconcileFromResolver(ctx)
			}
		}
	}()
}

func (s *Server) reconcileFromResolver(ctx context.Context) {
	backends, err := s.resolver.Snapshot(ctx)
	if err == nil {
		s.reconcileBackendState(backends)
	}
}

func (s *Server) proxy(writer http.ResponseWriter, request *http.Request) {
	startedAt := time.Now()
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
	if !s.tryAdmit() {
		status = http.StatusTooManyRequests
		s.metrics.admissionRejections.Inc()
		writer.Header().Set("Retry-After", "1")
		writer.Header().Set("X-Request-ID", requestID)
		writer.Header().Set("X-FishMesh-Route-Reason", "admission-capacity")
		http.Error(writer, "gateway concurrency limit reached", status)
		return
	}
	defer s.releaseAdmission()
	s.metrics.inflight.Inc()
	defer s.metrics.inflight.Dec()

	decision := s.route(request.Context(), request.Header.Get("X-FishMesh-Prefix-Key"))
	defer decision.release()
	s.metrics.routingDecisions.WithLabelValues(s.config.RoutingMode, decision.backend.ID).Inc()
	s.metrics.routingReasons.WithLabelValues(decision.reason).Inc()
	if decision.spilloverReason != "" {
		s.metrics.affinitySpillovers.WithLabelValues(decision.spilloverReason).Inc()
	}

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
		if request.Context().Err() != nil {
			// Client cancellation is a downstream outcome. It must release all
			// state but must not poison the selected backend's circuit.
			status = 499
			return
		}
		s.recordTransportOutcome(decision.backend.ID, true)
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
			http.Error(writer, "inference upstream timed out", status)
			return
		}
		status = http.StatusBadGateway
		http.Error(writer, "inference upstream unavailable", status)
		return
	}
	defer response.Body.Close()

	copyResponseHeaders(writer.Header(), response.Header)
	writer.Header().Set("X-Request-ID", requestID)
	writer.Header().Set("X-FishMesh-Routing-Mode", s.config.RoutingMode)
	writer.Header().Set("X-FishMesh-Route-Reason", decision.reason)
	writer.Header().Set("X-FishMesh-Backend-ID", decision.backend.ID)
	writer.Header().Set("X-FishMesh-Preferred-Backend-ID", decision.preferredBackendID)
	writer.Header().Set("X-FishMesh-Policy", decision.policy)
	if decision.spilloverReason != "" {
		writer.Header().Set("X-FishMesh-Spillover-Reason", decision.spilloverReason)
	}
	writer.Header().Set("X-FishMesh-Upstream", decision.upstream.Host)
	status = response.StatusCode
	writer.WriteHeader(status)

	detector := &sseDetector{}
	firstEventRecorded := false
	copyResult := copyResponseBody(writer, response.Body, func() {
		if !firstEventRecorded {
			firstEventRecorded = true
			s.metrics.ttftSeconds.Observe(time.Since(startedAt).Seconds())
		}
	}, detector)
	if copyResult.upstreamError != nil && request.Context().Err() == nil {
		s.metrics.streamErrors.Inc()
		s.recordTransportOutcome(decision.backend.ID, true)
		s.logger.Warn("upstream stream failed after response headers",
			"request_id", requestID, "backend_id", decision.backend.ID, "error", copyResult.upstreamError)
	} else if copyResult.downstreamError == nil {
		s.recordTransportOutcome(decision.backend.ID, false)
	}
}

func (s *Server) tryAdmit() bool {
	select {
	case s.admission <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releaseAdmission() { <-s.admission }

func (s *Server) route(ctx context.Context, prefixKey string) routeDecision {
	backends, err := s.resolver.Snapshot(ctx)
	if err != nil {
		backends = nil
	} else {
		s.reconcileBackendState(backends)
	}
	snapshot := routing.Snapshot{
		Backends: backends, Inflight: make(map[string]int64), Ineligible: make(map[string]string),
	}
	for _, backend := range backends {
		open := s.circuits.IsOpen(backend.ID)
		s.metrics.UpdateCircuit(backend.ID, open)
		if open {
			snapshot.Ineligible[backend.ID] = "circuit-open"
		}
	}
	if s.observations != nil {
		snapshot.Observations = observationsForBackends(s.observations.Snapshot(), backends)
		s.metrics.UpdateBackendObservations(snapshot.Observations)
	}
	s.metrics.UpdateEndpointDiscovery(s.resolver.Status())
	s.inflightMu.RLock()
	for id, value := range s.inflight {
		snapshot.Inflight[id] = value.Load()
	}
	s.inflightMu.RUnlock()
	var decision routing.Decision
	strategyErr := err
	if !s.directRoutingEligible() {
		decision = routing.Decision{
			Backend: s.service, PreferredBackendID: s.service.ID,
			Reason: "discovery-fallback", Policy: "service-fallback-v1",
		}
		s.metrics.routeFallbacks.Inc()
		strategyErr = nil
	} else if len(routing.EligibleBackends(snapshot)) == 0 {
		decision = routing.Decision{
			Backend: s.service, PreferredBackendID: s.service.ID,
			Reason: "circuit-fallback", Policy: "service-fallback-v1",
		}
		s.metrics.routeFallbacks.Inc()
		strategyErr = nil
	} else {
		decision, strategyErr = s.strategy.Select(prefixKey, snapshot)
	}
	if strategyErr != nil || decision.Backend.ID == "" {
		decision = routing.Decision{Backend: s.service, PreferredBackendID: s.service.ID, Reason: "strategy-fallback", Policy: "service-fallback-v1"}
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
		decision = routing.Decision{Backend: s.service, PreferredBackendID: s.service.ID, Reason: "backend-fallback", Policy: "service-fallback-v1"}
		upstream = s.backendURLs[s.service.ID]
		s.metrics.routeFallbacks.Inc()
	}
	state := s.inflightCounter(decision.Backend.ID)
	state.Add(1)
	backend := decision.Backend
	return routeDecision{
		backend: backend, preferredBackendID: decision.PreferredBackendID,
		reason: decision.Reason, spilloverReason: decision.SpilloverReason, policy: decision.Policy,
		upstream: upstream, client: s.pool.ClientFor(backend),
		release: func() { s.releaseBackend(backend.ID, state) },
	}
}

func observationsForBackends(states map[string]routing.BackendObservation, backends []routing.Backend) map[string]routing.BackendObservation {
	active := make(map[string]struct{}, len(backends))
	for _, backend := range backends {
		active[backend.ID] = struct{}{}
	}
	result := make(map[string]routing.BackendObservation, len(active))
	for id, state := range states {
		if _, ok := active[id]; ok {
			result[id] = state
		}
	}
	return result
}

func (s *Server) reconcileBackendState(backends []routing.Backend) {
	if reconciler, ok := s.strategy.(routing.BackendReconciler); ok {
		reconciler.ReconcileBackends(backends)
	}
	s.circuits.Reconcile(backends)
	active := make(map[string]struct{}, len(backends)+1)
	active[s.service.ID] = struct{}{}
	for _, backend := range backends {
		active[backend.ID] = struct{}{}
	}
	var removed []string
	s.inflightMu.Lock()
	s.active = active
	for id, counter := range s.inflight {
		if _, ok := active[id]; ok || counter.Load() != 0 {
			continue
		}
		delete(s.inflight, id)
		removed = append(removed, id)
	}
	s.inflightMu.Unlock()
	for _, id := range removed {
		s.cleanupBackend(id)
	}
}

func (s *Server) releaseBackend(backendID string, counter *atomic.Int64) {
	if counter.Add(-1) != 0 {
		return
	}
	removed := false
	s.inflightMu.Lock()
	if _, active := s.active[backendID]; !active && s.inflight[backendID] == counter {
		delete(s.inflight, backendID)
		removed = true
	}
	s.inflightMu.Unlock()
	if removed {
		s.cleanupBackend(backendID)
	}
}

func (s *Server) cleanupBackend(backendID string) {
	s.pool.Remove(backendID)
	s.circuits.Remove(backendID)
	s.metrics.DeleteBackend(backendID, s.config.RoutingMode)
}

func (s *Server) recordTransportOutcome(backendID string, failed bool) {
	if backendID == "" || backendID == s.service.ID {
		return
	}
	if opened := s.circuits.Record(backendID, failed); opened {
		s.metrics.CircuitOpened(backendID)
	} else {
		s.metrics.UpdateCircuit(backendID, s.circuits.IsOpen(backendID))
	}
}

func (s *Server) directRoutingEligible() bool {
	if s.config.EndpointDiscovery != "endpointslice" {
		return true
	}
	status := s.resolver.Status()
	return status.Status != routing.ObservationUnavailable && status.ReadyBackends > 0 && status.Freshness <= s.config.EndpointMaxAge
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

type bodyCopyResult struct {
	upstreamError   error
	downstreamError error
}

func copyResponseBody(writer http.ResponseWriter, body io.Reader, onFirstEvent func(), detector *sseDetector) bodyCopyResult {
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
				return bodyCopyResult{downstreamError: writeErr}
			}
			if flusher, ok := writer.(http.Flusher); ok {
				if flushErr := bufferedWriter.Flush(); flushErr != nil {
					return bodyCopyResult{downstreamError: flushErr}
				}
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return bodyCopyResult{}
			}
			return bodyCopyResult{upstreamError: readErr}
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
