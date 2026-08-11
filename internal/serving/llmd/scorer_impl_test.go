package llmd

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"

	envoytype "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	errcommon "github.com/llm-d/llm-d-router/pkg/common/error"
	lldmdatalayer "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	lldmrequestcontrol "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requestcontrol"
	lldmscheduling "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
	attrconcurrency "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/datalayer/attribute/concurrency"
	maxscore "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/scheduling/picker/maxscore"
	upstreamscheduling "github.com/llm-d/llm-d-router/pkg/epp/scheduling"
	"k8s.io/apimachinery/pkg/types"
)

const testRoutingKey = "session-a"

func TestScorerDeclaresRequiredInflightDependency(t *testing.T) {
	created := newTestScorer(t, time.Now)
	dependencies := created.Consumes()
	if _, ok := dependencies.Required[created.inflightKey]; !ok {
		t.Fatalf("required dependencies = %+v", dependencies.Required)
	}
	if created.Category() != lldmscheduling.Balance {
		t.Fatalf("category = %v", created.Category())
	}
}

func TestFilterRejectsIncompleteEndpointState(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	created := newTestScorer(t, func() time.Time { return now })
	valid := testEndpoint("valid", "10.0.0.1", 1, 0, now)
	missingInflight := testEndpointWithoutInflight("missing-load", "10.0.0.2", now)
	invalidAddress := testEndpoint("invalid-address", "", 0, 0, now)

	filtered := created.Filter(context.Background(), &lldmscheduling.InferenceRequest{}, []lldmscheduling.Endpoint{
		valid, missingInflight, invalidAddress, nil,
	})
	if len(filtered) != 1 || filtered[0] != valid {
		t.Fatalf("filtered endpoints = %v, want only valid endpoint", filtered)
	}
}

func TestScorePreservesSessionKeyAndEmitsDecisionHeaders(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	created := newTestScorer(t, func() time.Time { return now })
	request := testRequest(testRoutingKey)
	initial := []lldmscheduling.Endpoint{
		testEndpoint("model-a", "10.0.0.1", 0, 0, now),
		testEndpoint("model-b", "10.0.0.2", 0, 0, now),
	}

	preferred := selectedEndpoint(t, created.Score(context.Background(), request, initial))
	other := otherEndpoint(t, preferred, initial)
	pressured := []lldmscheduling.Endpoint{
		endpointWithLoad(preferred, 10, 0, now),
		endpointWithLoad(other, 0, 0, now),
	}
	selected := selectedEndpoint(t, created.Score(context.Background(), request, pressured))
	if endpointID(selected) != endpointID(other) {
		t.Fatalf("selected endpoint = %s, want spillover %s", endpointID(selected), endpointID(other))
	}

	response := &lldmrequestcontrol.Response{Headers: map[string]string{}}
	// 模拟数据面 retry 到 preferred，确认决策 endpoint 与实际服务 endpoint 不会混写。
	created.ResponseHeader(context.Background(), request, response, preferred.GetMetadata())
	if response.Headers[HeaderRouteReason] != string(routing.ReasonSessionKeySpillover) {
		t.Fatalf("route reason = %q", response.Headers[HeaderRouteReason])
	}
	if response.Headers[HeaderSpilloverReason] != string(routing.ReasonLocalInflight) {
		t.Fatalf("spillover reason = %q", response.Headers[HeaderSpilloverReason])
	}
	if response.Headers[HeaderPreferredBackendID] != string(endpointID(preferred)) {
		t.Fatalf("preferred backend = %q", response.Headers[HeaderPreferredBackendID])
	}
	if response.Headers[HeaderSelectedBackendID] != string(endpointID(other)) {
		t.Fatalf("selected backend = %q", response.Headers[HeaderSelectedBackendID])
	}
	if response.Headers[HeaderServedBackendID] != string(endpointID(preferred)) ||
		response.Headers[HeaderBackendID] != string(endpointID(preferred)) {
		t.Fatalf("served headers = %+v", response.Headers)
	}
}

func TestScoreIgnoresStaleQueueMetricsAndResumesSessionKey(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	created := newTestScorer(t, func() time.Time { return now })
	request := testRequest("queue-session")
	initial := []lldmscheduling.Endpoint{
		testEndpoint("model-a", "10.0.0.1", 0, 0, now),
		testEndpoint("model-b", "10.0.0.2", 0, 0, now),
	}
	preferred := selectedEndpoint(t, created.Score(context.Background(), request, initial))
	other := otherEndpoint(t, preferred, initial)

	fresh := []lldmscheduling.Endpoint{
		endpointWithLoad(preferred, 0, 10, now),
		endpointWithLoad(other, 0, 0, now),
	}
	if selected := selectedEndpoint(t, created.Score(context.Background(), request, fresh)); endpointID(selected) != endpointID(other) {
		t.Fatalf("fresh queue selected = %s, want %s", endpointID(selected), endpointID(other))
	}

	staleAt := now.Add(-testConfig().MetricsMaxAge - time.Second)
	stale := []lldmscheduling.Endpoint{
		endpointWithLoad(preferred, 0, 10, staleAt),
		endpointWithLoad(other, 0, 0, staleAt),
	}
	if selected := selectedEndpoint(t, created.Score(context.Background(), request, stale)); endpointID(selected) != endpointID(preferred) {
		t.Fatalf("stale queue selected = %s, want  session-key %s", endpointID(selected), endpointID(preferred))
	}
}

func TestScoreWithoutRoutingKeyChoosesLeastLoaded(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	created := newTestScorer(t, func() time.Time { return now })
	request := testRequest("")
	endpoints := []lldmscheduling.Endpoint{
		testEndpoint("busy", "10.0.0.1", 5, 0, now),
		testEndpoint("idle", "10.0.0.2", 0, 0, now),
	}

	selected := selectedEndpoint(t, created.Score(context.Background(), request, endpoints))
	if endpointID(selected) != endpointID(endpoints[1]) {
		t.Fatalf("selected = %s, want least-loaded %s", endpointID(selected), endpointID(endpoints[1]))
	}
	record, ok := lldmscheduling.ReadRequestAttribute[decisionRecord](request, created.attributeKey)
	if !ok || record.Decision.Reason != routing.ReasonMissingSessionKeyLoadBalanced {
		t.Fatalf("decision = %+v, want missing-key reason", record.Decision)
	}
}

func TestScoreReconcilesEndpointChurn(t *testing.T) {
	created := newTestScorer(t, time.Now)
	request := testRequest("churn-session")
	initial := []lldmscheduling.Endpoint{
		testEndpoint("model-a", "10.0.0.1", 0, 0, time.Now()),
		testEndpoint("model-b", "10.0.0.2", 0, 0, time.Now()),
	}
	removed := selectedEndpoint(t, created.Score(context.Background(), request, initial))
	remaining := otherEndpoint(t, removed, initial)

	selected := selectedEndpoint(t, created.Score(context.Background(), request, []lldmscheduling.Endpoint{remaining}))
	if endpointID(selected) != endpointID(remaining) {
		t.Fatalf("selected removed endpoint: got %s, want %s", endpointID(selected), endpointID(remaining))
	}
}

func TestScorerUsesStableIdentityAcrossInstancesAndConcurrentRequests(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	first := newTestScorer(t, func() time.Time { return now })
	second := newTestScorer(t, func() time.Time { return now })
	endpoints := []lldmscheduling.Endpoint{
		testEndpoint("model-a", "10.0.0.1", 0, 0, now),
		testEndpoint("model-b", "10.0.0.2", 0, 0, now),
	}
	firstSelected := selectedEndpoint(t, first.Score(context.Background(), testRequest(testRoutingKey), endpoints))
	secondSelected := selectedEndpoint(t, second.Score(context.Background(), testRequest(testRoutingKey), endpoints))
	if endpointID(firstSelected) != endpointID(secondSelected) {
		t.Fatalf("instance selections differ: %s != %s", endpointID(firstSelected), endpointID(secondSelected))
	}

	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request := testRequest(fmt.Sprintf("session-%d", index%8))
			selectedEndpoint(t, first.Score(context.Background(), request, endpoints))
		}(index)
	}
	wait.Wait()
}

func TestSchedulerProfileBoundaryRejectsInvalidCandidates(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	created := newTestScorer(t, func() time.Time { return now })
	profile := upstreamscheduling.NewSchedulerProfile().
		WithFilters(created).
		WithScorers(upstreamscheduling.NewWeightedScorer(created, 1)).
		WithPicker(maxscore.NewMaxScorePicker(1))
	valid := []lldmscheduling.Endpoint{
		testEndpoint("model-a", "10.0.0.1", 0, 0, now),
		testEndpoint("model-b", "10.0.0.2", 1, 0, now),
	}

	result, err := profile.Run(context.Background(), testRequest("profile-session"), valid)
	if err != nil {
		t.Fatalf("profile.Run(valid) error = %v", err)
	}
	if len(result.TargetEndpoints) != 1 {
		t.Fatalf("target endpoints = %d, want 1", len(result.TargetEndpoints))
	}
	if _, err := profile.Run(context.Background(), testRequest("profile-session"), nil); err == nil {
		t.Fatal("profile.Run(empty) error = nil")
	}
	invalid := []lldmscheduling.Endpoint{testEndpointWithoutInflight("missing-load", "10.0.0.3", now)}
	if _, err := profile.Run(context.Background(), testRequest("profile-session"), invalid); err == nil {
		t.Fatal("profile.Run(invalid) error = nil")
	}
}

func TestIntegratedSelectionConformsToPureRoutingPolicy(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	config := testConfig()
	config.Clock = func() time.Time { return now }
	plugin, err := New("conformance", config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	created := plugin.(*scorer)
	expectedStrategy, err := routing.NewSessionKey(normalizeConfig(config).SessionKey)
	if err != nil {
		t.Fatalf("routing.NewSessionKey() error = %v", err)
	}
	expectedReconciler := expectedStrategy.(routing.BackendReconciler)
	request := testRequest("conformance-session")
	cycles := [][]lldmscheduling.Endpoint{
		{
			testEndpoint("model-a", "10.0.0.1", 0, 0, now),
			testEndpoint("model-b", "10.0.0.2", 0, 0, now),
		},
	}
	preferred := selectedEndpoint(t, created.Score(context.Background(), request, cycles[0]))
	other := otherEndpoint(t, preferred, cycles[0])
	cycles = append(cycles, []lldmscheduling.Endpoint{
		endpointWithLoad(preferred, 10, 0, now),
		endpointWithLoad(other, 0, 0, now),
	})

	// 重新创建 integrated scorer，使两边从完全相同的 registry 状态开始执行同一序列。
	plugin, err = New("conformance-sequence", config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	created = plugin.(*scorer)
	for index, endpoints := range cycles {
		snapshot, _ := translateSnapshot(endpoints, created.inflightKey.String(), now, created.metricsMaxAge)
		expectedReconciler.ReconcileBackends(snapshot.Backends)
		expected, err := expectedStrategy.Select("conformance-session", snapshot)
		if err != nil {
			t.Fatalf("cycle %d routing.Select() error = %v", index, err)
		}
		actual := selectedEndpoint(t, created.Score(context.Background(), request, endpoints))
		if endpointID(actual) != expected.Backend.ID {
			t.Fatalf("cycle %d integrated = %s, pure routing = %s", index, endpointID(actual), expected.Backend.ID)
		}
		record, ok := lldmscheduling.ReadRequestAttribute[decisionRecord](request, created.attributeKey)
		if !ok || record.Decision.Reason != expected.Reason || record.Decision.SpilloverReason != expected.SpilloverReason {
			t.Fatalf("cycle %d decision = %+v, expected %+v", index, record.Decision, expected)
		}
	}
}

func TestPinnedUpstreamMapsEmptyCandidateContractTo503(t *testing.T) {
	response, err := errcommon.BuildErrResponse(errcommon.Error{
		Code: errcommon.ServiceUnavailable,
		Msg:  "failed to find endpoint candidates for serving the request",
	})
	if err != nil {
		t.Fatalf("BuildErrResponse() error = %v", err)
	}
	if got := response.GetImmediateResponse().GetStatus().GetCode(); got != envoytype.StatusCode_ServiceUnavailable {
		t.Fatalf("status = %v, want 503 ServiceUnavailable", got)
	}
}

func TestConfiguredClockOverridesNestedRoutingClock(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	config := testConfig()
	config.Clock = func() time.Time { return now }
	config.SessionKey.Clock = func() time.Time { panic("nested clock must be replaced") }
	plugin, err := New("clock", config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	created := plugin.(*scorer)
	endpoints := []lldmscheduling.Endpoint{testEndpoint("model-a", "10.0.0.1", 0, 0, now)}
	selectedEndpoint(t, created.Score(context.Background(), testRequest("clock-session"), endpoints))
}

func newTestScorer(t *testing.T, clock Clock) *scorer {
	t.Helper()
	config := testConfig()
	config.Clock = clock
	plugin, err := New("test", config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return plugin.(*scorer)
}

func testRequest(key string) *lldmscheduling.InferenceRequest {
	return &lldmscheduling.InferenceRequest{Headers: map[string]string{testConfig().SessionKeyHeader: key}}
}

func testEndpoint(name, address string, inflight int64, queue int, observedAt time.Time) lldmscheduling.Endpoint {
	attributes := lldmdatalayer.NewAttributes()
	attributes.Put(attrconcurrency.InFlightLoadDataKey.String(), &attrconcurrency.InFlightLoad{Requests: inflight})
	return lldmscheduling.NewEndpoint(
		&lldmdatalayer.EndpointMetadata{
			NamespacedName: types.NamespacedName{Namespace: "fishmesh-system", Name: name},
			PodName:        name,
			Address:        address,
			Port:           "8000",
		},
		&lldmdatalayer.Metrics{WaitingQueueSize: queue, UpdateTime: observedAt},
		attributes,
	)
}

func testEndpointWithoutInflight(name, address string, observedAt time.Time) lldmscheduling.Endpoint {
	return lldmscheduling.NewEndpoint(
		&lldmdatalayer.EndpointMetadata{
			NamespacedName: types.NamespacedName{Namespace: "fishmesh-system", Name: name},
			PodName:        name,
			Address:        address,
			Port:           "8000",
		},
		&lldmdatalayer.Metrics{UpdateTime: observedAt},
		nil,
	)
}

func endpointWithLoad(endpoint lldmscheduling.Endpoint, inflight int64, queue int, observedAt time.Time) lldmscheduling.Endpoint {
	metadata := endpoint.GetMetadata()
	return testEndpoint(metadata.PodName, metadata.Address, inflight, queue, observedAt)
}

func selectedEndpoint(t *testing.T, scores map[lldmscheduling.Endpoint]float64) lldmscheduling.Endpoint {
	t.Helper()
	var selected lldmscheduling.Endpoint
	for endpoint, score := range scores {
		if score != selectedScore {
			continue
		}
		if selected != nil {
			t.Fatal("more than one endpoint received selected score")
		}
		selected = endpoint
	}
	if selected == nil {
		t.Fatal("no endpoint received selected score")
	}
	return selected
}

func otherEndpoint(t *testing.T, selected lldmscheduling.Endpoint, endpoints []lldmscheduling.Endpoint) lldmscheduling.Endpoint {
	t.Helper()
	for _, endpoint := range endpoints {
		if endpointID(endpoint) != endpointID(selected) {
			return endpoint
		}
	}
	t.Fatal("other endpoint not found")
	return nil
}

func endpointID(endpoint lldmscheduling.Endpoint) backend.ID {
	return servedBackendID(endpoint.GetMetadata())
}
