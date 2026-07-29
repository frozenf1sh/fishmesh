package domain

import (
	"testing"
	"time"
)

func TestRulePolicyPrioritizesNetworkOverPrefixLocality(t *testing.T) {
	policy := DefaultRulePolicy()
	diagnosis := policy.Evaluate(Incident{}, []Signal{
		{Name: "query_ebpf_network", Values: map[string]float64{"tcp_rtt_ms": 180}},
		{Name: "query_llm_metrics", Values: map[string]float64{"prefix_cache_hit_rate": 0.1}},
	}, time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	if diagnosis.Code != "network_degraded" {
		t.Fatalf("diagnosis code = %q, want network_degraded", diagnosis.Code)
	}
}

func TestRulePolicyDoesNotTreatMissingSignalsAsHealthy(t *testing.T) {
	policy := DefaultRulePolicy()
	diagnosis := policy.Evaluate(Incident{}, []Signal{
		{Name: "query_gateway_stats", Status: SignalOK},
		{Name: "query_llm_metrics", Status: SignalOK, Values: map[string]float64{"prefix_cache_hit_rate": 0.1}},
		{Name: "query_gpu_status", Status: SignalUnavailable},
		{Name: "query_ebpf_network", Status: SignalUnavailable},
	}, time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	if diagnosis.Code != "insufficient_observability" {
		t.Fatalf("diagnosis code = %q, want insufficient_observability", diagnosis.Code)
	}
}
