package llmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"

	lldmscheduling "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
	attrconcurrency "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/datalayer/attribute/concurrency"
)

const observationSource = "llm-d"

type translatedEndpoint struct {
	upstream lldmscheduling.Endpoint
	backend  backend.Backend
	inflight int64
	queue    observation.Backend
}

func translateEndpoint(endpoint lldmscheduling.Endpoint, inflightKey string, now time.Time, maxAge time.Duration) (translatedEndpoint, error) {
	if endpoint == nil {
		return translatedEndpoint{}, fmt.Errorf("llm-d endpoint must not be nil")
	}
	candidate, err := backendFromMetadata(endpoint)
	if err != nil {
		return translatedEndpoint{}, err
	}
	inflight, err := endpointInflight(endpoint, inflightKey)
	if err != nil {
		return translatedEndpoint{}, err
	}
	return translatedEndpoint{
		upstream: endpoint,
		backend:  candidate,
		inflight: inflight,
		queue:    endpointQueue(endpoint, now, maxAge),
	}, nil
}

func translateSnapshot(endpoints []lldmscheduling.Endpoint, inflightKey string, now time.Time, maxAge time.Duration) (routing.Snapshot, map[backend.ID]lldmscheduling.Endpoint) {
	snapshot := routing.Snapshot{
		Backends:     make([]backend.Backend, 0, len(endpoints)),
		Inflight:     make(map[backend.ID]int64, len(endpoints)),
		Observations: make(map[backend.ID]observation.Backend, len(endpoints)),
		Ineligible:   map[backend.ID]routing.Reason{},
	}
	lookup := make(map[backend.ID]lldmscheduling.Endpoint, len(endpoints))
	for _, endpoint := range endpoints {
		state, err := translateEndpoint(endpoint, inflightKey, now, maxAge)
		if err != nil {
			continue
		}
		if _, exists := lookup[state.backend.ID]; exists {
			continue
		}
		lookup[state.backend.ID] = state.upstream
		snapshot.Backends = append(snapshot.Backends, state.backend)
		snapshot.Inflight[state.backend.ID] = state.inflight
		snapshot.Observations[state.backend.ID] = state.queue
	}
	return snapshot, lookup
}

func backendFromMetadata(endpoint lldmscheduling.Endpoint) (backend.Backend, error) {
	metadata := endpoint.GetMetadata()
	if metadata == nil {
		return backend.Backend{}, fmt.Errorf("llm-d endpoint metadata must not be nil")
	}
	address := strings.TrimSpace(metadata.Address)
	port, err := strconv.Atoi(strings.TrimSpace(metadata.Port))
	if address == "" || err != nil || port <= 0 || port > 65535 {
		return backend.Backend{}, fmt.Errorf("llm-d endpoint address must be valid: %q:%q", metadata.Address, metadata.Port)
	}
	podName := strings.TrimSpace(metadata.PodName)
	if podName == "" {
		podName = strings.TrimSpace(metadata.NamespacedName.Name)
	}
	values := map[string]string{}
	if podName != "" {
		values[backend.MetadataPodName] = podName
	}
	return backend.NewHTTP(address, port, values), nil
}

func endpointInflight(endpoint lldmscheduling.Endpoint, key string) (int64, error) {
	value, ok := endpoint.Get(key)
	if !ok {
		return 0, fmt.Errorf("llm-d endpoint is missing required in-flight load")
	}
	load, ok := value.(*attrconcurrency.InFlightLoad)
	if !ok || load.Requests < 0 {
		return 0, fmt.Errorf("llm-d endpoint has invalid in-flight load")
	}
	return load.Requests, nil
}

func endpointQueue(endpoint lldmscheduling.Endpoint, now time.Time, maxAge time.Duration) observation.Backend {
	result := observation.Backend{Status: observation.StatusDegraded, Source: observationSource}
	metrics := endpoint.GetMetrics()
	if metrics == nil || metrics.UpdateTime.IsZero() || metrics.UpdateTime.After(now) || now.Sub(metrics.UpdateTime) > maxAge || metrics.WaitingQueueSize < 0 {
		return result
	}
	result.Status = observation.StatusOK
	result.ObservedAt = metrics.UpdateTime
	result.Freshness = now.Sub(metrics.UpdateTime)
	result.QueueLength = observation.Sample[float64]{
		Value: float64(metrics.WaitingQueueSize), Valid: true,
		ObservedAt: metrics.UpdateTime, Source: observationSource,
	}
	return result
}
