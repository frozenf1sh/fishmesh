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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server is a transparent OpenAI-compatible proxy. The baseline deliberately creates
// a fresh upstream connection for every client request, leaving replica selection to the
// Kubernetes ClusterIP Service.
type Server struct {
	config   Config
	upstream *url.URL
	client   *http.Client
	metrics  *metrics
	logger   *slog.Logger
	registry *prometheus.Registry
}

func NewServer(config Config, logger *slog.Logger) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	upstream, err := url.Parse(config.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	registry := prometheus.NewRegistry()
	return &Server{
		config:   config,
		upstream: upstream,
		client: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DisableKeepAlives:     true,
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          0,
			IdleConnTimeout:       0,
			ResponseHeaderTimeout: config.RequestTimeout,
		}},
		metrics:  newMetrics(registry),
		logger:   logger,
		registry: registry,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/v1/", s.proxy)
	return mux
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
	upstreamRequest, cancel, err := s.newUpstreamRequest(request, requestID)
	if err != nil {
		http.Error(writer, "invalid upstream request", http.StatusBadRequest)
		status = http.StatusBadRequest
		return
	}
	defer cancel()

	response, err := s.client.Do(upstreamRequest)
	if err != nil {
		s.metrics.upstreamErrors.Inc()
		s.logger.Warn("upstream request failed", "request_id", requestID, "error", err)
		http.Error(writer, "inference upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	copyResponseHeaders(writer.Header(), response.Header)
	writer.Header().Set("X-Request-ID", requestID)
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

func (s *Server) newUpstreamRequest(request *http.Request, requestID string) (*http.Request, context.CancelFunc, error) {
	contextWithTimeout, cancel := context.WithTimeout(request.Context(), s.config.RequestTimeout)
	upstreamRequest := request.Clone(contextWithTimeout)
	upstreamRequest.URL.Scheme = s.upstream.Scheme
	upstreamRequest.URL.Host = s.upstream.Host
	upstreamRequest.URL.Path = joinURLPath(s.upstream.Path, request.URL.Path)
	upstreamRequest.URL.RawPath = ""
	upstreamRequest.Host = s.upstream.Host
	upstreamRequest.RequestURI = ""
	removeHopByHopHeaders(upstreamRequest.Header)
	upstreamRequest.Header.Set("X-Request-ID", requestID)
	upstreamRequest.Header.Set("Connection", "close")
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
