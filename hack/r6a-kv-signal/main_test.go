package main

import "testing"

func TestBackendFlagsSet(t *testing.T) {
	var backends backendFlags
	if err := backends.Set("10.0.0.1:8000,http://10.0.0.1:8000,tcp://10.0.0.1:5557,tcp://10.0.0.1:5558"); err != nil {
		t.Fatalf("解析合法 backend: %v", err)
	}
	if len(backends) != 1 {
		t.Fatalf("backend 数量 = %d，期望 1", len(backends))
	}
	if backends[0].ID != "10.0.0.1:8000" {
		t.Fatalf("backend ID = %q", backends[0].ID)
	}
}

func TestBackendFlagsRejectsIncompleteValue(t *testing.T) {
	var backends backendFlags
	if err := backends.Set("pod,http://pod:8000,tcp://pod:5557"); err == nil {
		t.Fatal("缺少 replay endpoint 时应拒绝配置")
	}
}
