package llmd

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"

	lldmdatalayer "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	lldmplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	lldmrequestcontrol "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requestcontrol"
	lldmscheduling "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
	attrconcurrency "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/datalayer/attribute/concurrency"
)

const (
	selectedScore   = 1.0
	unselectedScore = 0.0
)

var (
	_ lldmscheduling.Filter                      = (*scorer)(nil)
	_ lldmscheduling.Scorer                      = (*scorer)(nil)
	_ lldmplugin.ConsumerPlugin                  = (*scorer)(nil)
	_ lldmrequestcontrol.ResponseHeaderProcessor = (*scorer)(nil)
)

type decisionRecord struct {
	Decision routing.Decision
}

type scorer struct {
	typedName        lldmplugin.TypedName
	routingKeyHeader string
	metricsMaxAge    time.Duration
	clock            Clock
	strategy         routing.Strategy
	reconciler       routing.BackendReconciler
	inflightKey      lldmplugin.DataKey
	attributeKey     string
}

func newScorer(name string, config Config, strategy routing.Strategy) *scorer {
	result := &scorer{
		typedName:        lldmplugin.TypedName{Type: PluginType, Name: name},
		routingKeyHeader: config.RoutingKeyHeader,
		metricsMaxAge:    config.MetricsMaxAge,
		clock:            config.Clock,
		strategy:         strategy,
		inflightKey:      attrconcurrency.InFlightLoadDataKey.WithNonEmptyProducerName(config.InFlightLoadProducerName),
		attributeKey:     decisionAttributePrefix + name,
	}
	result.reconciler, _ = strategy.(routing.BackendReconciler)
	return result
}

func (s *scorer) TypedName() lldmplugin.TypedName {
	return s.typedName
}

func (*scorer) Category() lldmscheduling.ScorerCategory {
	return lldmscheduling.Balance
}

func (s *scorer) Consumes() lldmplugin.DataDependencies {
	return lldmplugin.DataDependencies{
		Required: map[lldmplugin.DataKey]any{s.inflightKey: attrconcurrency.InFlightLoad{}},
	}
}

func (s *scorer) Filter(_ context.Context, _ *lldmscheduling.InferenceRequest, endpoints []lldmscheduling.Endpoint) []lldmscheduling.Endpoint {
	now := s.clock()
	filtered := make([]lldmscheduling.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if _, err := translateEndpoint(endpoint, s.inflightKey.String(), now, s.maxAge()); err == nil {
			filtered = append(filtered, endpoint)
		}
	}
	return filtered
}

func (s *scorer) Score(_ context.Context, request *lldmscheduling.InferenceRequest, endpoints []lldmscheduling.Endpoint) map[lldmscheduling.Endpoint]float64 {
	scores := make(map[lldmscheduling.Endpoint]float64, len(endpoints))
	for _, endpoint := range endpoints {
		scores[endpoint] = unselectedScore
	}
	snapshot, lookup := translateSnapshot(endpoints, s.inflightKey.String(), s.clock(), s.maxAge())
	if len(snapshot.Backends) == 0 {
		return scores
	}
	if s.reconciler != nil {
		s.reconciler.ReconcileBackends(snapshot.Backends)
	}
	decision, err := s.strategy.Select(s.routingKey(request), snapshot)
	if err != nil {
		return scores
	}
	selected, ok := lookup[decision.Backend.ID]
	if !ok {
		return scores
	}
	scores[selected] = selectedScore
	if request != nil {
		request.PutAttribute(s.attributeKey, decisionRecord{Decision: decision})
	}
	return scores
}

func (s *scorer) ResponseHeader(_ context.Context, request *lldmscheduling.InferenceRequest, response *lldmrequestcontrol.Response, served *lldmdatalayer.EndpointMetadata) {
	if request == nil || response == nil || response.Headers == nil {
		return
	}
	record, ok := lldmscheduling.ReadRequestAttribute[decisionRecord](request, s.attributeKey)
	if !ok {
		return
	}
	writeDecisionHeaders(response.Headers, record.Decision)
	if servedID := servedBackendID(served); servedID != "" {
		response.Headers[HeaderBackendID] = string(servedID)
		response.Headers[HeaderServedBackendID] = string(servedID)
	}
}

func (s *scorer) routingKey(request *lldmscheduling.InferenceRequest) string {
	if request == nil {
		return ""
	}
	return request.Headers[s.routingKeyHeader]
}

func (s *scorer) maxAge() time.Duration {
	return s.metricsMaxAge
}

func writeDecisionHeaders(headers map[string]string, decision routing.Decision) {
	headers[HeaderRoutingMode] = string(routing.ModeBoundedAffinity)
	headers[HeaderRouteReason] = string(decision.Reason)
	headers[HeaderSelectedBackendID] = string(decision.Backend.ID)
	headers[HeaderPreferredBackendID] = string(decision.PreferredBackendID)
	headers[HeaderPolicy] = string(decision.Policy)
	if decision.SpilloverReason != "" {
		headers[HeaderSpilloverReason] = string(decision.SpilloverReason)
	}
}

func servedBackendID(metadata *lldmdatalayer.EndpointMetadata) backend.ID {
	if metadata == nil {
		return ""
	}
	address := strings.TrimSpace(metadata.Address)
	port, err := strconv.Atoi(strings.TrimSpace(metadata.Port))
	if err != nil || address == "" || port <= 0 || port > 65535 {
		return ""
	}
	return backend.NewHTTP(address, port, nil).ID
}
