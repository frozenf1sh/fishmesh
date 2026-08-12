package requestpath

import (
	"testing"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
	"github.com/frozenf1sh/fishmesh/internal/serving/observation"
)

func TestRoutingLoadsPublishesQueueAndLocalHardOverload(t *testing.T) {
	backends := []backend.Backend{{ID: "a"}, {ID: "b"}}
	observations := map[backend.ID]observation.Backend{
		"a": {
			QueueLength:     observation.Sample[float64]{Value: 4, Valid: true},
			RunningRequests: observation.Sample[float64]{Value: 1, Valid: true},
		},
	}
	loads := routingLoads(backends, observations, map[backend.ID]int64{"b": 3}, Config{
		HardQueueDepth: 4, HardLocalInflight: 3,
	})

	if !loads["a"].Valid || !loads["a"].HardOverload {
		t.Fatalf("queue threshold was not published: %+v", loads["a"])
	}
	if loads["b"].Valid || !loads["b"].HardOverload {
		t.Fatalf("local threshold must work without an observation: %+v", loads["b"])
	}
}

func TestRoutingLoadsDoesNotTreatUnknownQueueAsOverloaded(t *testing.T) {
	backends := []backend.Backend{{ID: "a"}}
	observations := map[backend.ID]observation.Backend{
		"a": {
			QueueLength:     observation.Sample[float64]{Value: 100, Valid: false},
			RunningRequests: observation.Sample[float64]{Value: 1, Valid: true},
		},
	}
	load := routingLoads(backends, observations, nil, Config{HardQueueDepth: 1})["a"]
	if load.Valid || load.HardOverload {
		t.Fatalf("unknown queue triggered a hard gate: %+v", load)
	}
}
