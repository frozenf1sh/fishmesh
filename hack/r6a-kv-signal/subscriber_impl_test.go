package main

import (
	"testing"
	"time"
)

func TestStreamSnapshotRequiresFreshReplay(t *testing.T) {
	stream := &eventStream{
		backend:       backendConfig{ID: "pod-a:8000"},
		freshnessTTL:  time.Second,
		enabled:       true,
		lastReplayAt:  time.Now(),
		eventCounts:   map[string]uint64{"BlockStored": 2},
		invalidReason: "",
	}

	snapshot := stream.Snapshot()
	if !snapshot.Valid {
		t.Fatalf("新鲜 replay 心跳应有效，实际原因 %q", snapshot.InvalidReason)
	}

	// Snapshot 必须复制 map，调用方不能反向修改订阅 owner 的内部状态。
	snapshot.EventCounts["BlockStored"] = 99
	if stream.eventCounts["BlockStored"] != 2 {
		t.Fatal("Snapshot 暴露了内部 eventCounts map")
	}
}

func TestStreamSnapshotRejectsStaleReplay(t *testing.T) {
	stream := &eventStream{
		backend:      backendConfig{ID: "pod-a:8000"},
		freshnessTTL: time.Second,
		enabled:      true,
		lastReplayAt: time.Now().Add(-2 * time.Second),
		eventCounts:  map[string]uint64{},
	}

	snapshot := stream.Snapshot()
	if snapshot.Valid || snapshot.InvalidReason != "replay-heartbeat-stale" {
		t.Fatalf("过期 replay 状态不正确: %+v", snapshot)
	}
}
