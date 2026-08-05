package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents/engineadapter"
)

const (
	probeIndexKeys  = 100_000
	probePodsPerKey = 8
	probeWorkers    = 2
	maxControlBody  = 2 << 20
	canonicalBlock  = 16
)

// probe 把上游解析/索引能力与本实验的可靠订阅边界组合起来。
// 它不包含路由策略，也不代理真实业务流量。
type probe struct {
	ctx        context.Context
	indexer    *kvcache.Indexer
	eventPool  *kvevents.Pool
	backends   map[string]backendConfig
	streams    map[string]*eventStream
	vllmClient *vllmClient
}

type requestEnvelope struct {
	Backend string          `json:"backend"`
	Request json.RawMessage `json:"request"`
}

type streamControl struct {
	Backend string `json:"backend"`
	Enabled bool   `json:"enabled"`
}

type clearRequest struct {
	Backend string `json:"backend"`
}

type matchResult struct {
	Backend       string  `json:"backend"`
	MatchedBlocks float64 `json:"matched_blocks"`
	MatchedTokens int     `json:"matched_tokens"`
	Valid         bool    `json:"valid"`
	InvalidReason string  `json:"invalid_reason,omitempty"`
}

type scoreResponse struct {
	PromptTokens    int           `json:"prompt_tokens"`
	RenderLatencyMS float64       `json:"render_latency_ms"`
	LookupLatencyMS float64       `json:"lookup_latency_ms"`
	Matches         []matchResult `json:"matches"`
}

type stateResponse struct {
	Streams       []streamSnapshot `json:"streams"`
	HeapAllocByte uint64           `json:"heap_alloc_bytes"`
	HeapSysByte   uint64           `json:"heap_sys_bytes"`
}

func newProbe(
	ctx context.Context,
	backendConfigs []backendConfig,
	freshnessTTL time.Duration,
	replayPeriod time.Duration,
) (*probe, error) {
	indexerConfig, err := kvcache.NewDefaultConfig()
	if err != nil {
		return nil, fmt.Errorf("创建上游索引默认配置: %w", err)
	}
	// 默认 1 亿 key 对单机 spike 过大。R6A 只需要一个显式有界、足以触发回收观察的索引。
	indexerConfig.KVBlockIndexConfig.InMemoryConfig = &kvblock.InMemoryIndexConfig{
		Size:         probeIndexKeys,
		PodCacheSize: probePodsPerKey,
	}

	tokenProcessor, err := kvblock.NewChunkedTokenDatabase(&kvblock.TokenProcessorConfig{
		BlockSizeTokens: canonicalBlock,
	})
	if err != nil {
		return nil, fmt.Errorf("创建上游 token processor: %w", err)
	}

	indexer, err := kvcache.NewKVCacheIndexer(ctx, indexerConfig, tokenProcessor)
	if err != nil {
		return nil, fmt.Errorf("创建上游 KV indexer: %w", err)
	}
	go indexer.Run(ctx)

	adapter := engineadapter.NewVLLMAdapter()
	eventPool := kvevents.NewPool(&kvevents.Config{Concurrency: probeWorkers}, indexer.KVBlockIndex(), tokenProcessor, adapter)
	eventPool.Start(ctx)

	service := &probe{
		ctx:        ctx,
		indexer:    indexer,
		eventPool:  eventPool,
		backends:   make(map[string]backendConfig, len(backendConfigs)),
		streams:    make(map[string]*eventStream, len(backendConfigs)),
		vllmClient: newVLLMClient(),
	}
	for _, backend := range backendConfigs {
		if _, exists := service.backends[backend.ID]; exists {
			service.Close()
			return nil, fmt.Errorf("backend ID 重复: %s", backend.ID)
		}
		service.backends[backend.ID] = backend
		service.streams[backend.ID] = newEventStream(
			ctx, backend, eventPool, indexer.KVBlockIndex(), adapter, freshnessTTL, replayPeriod,
		)
	}
	return service, nil
}

func (p *probe) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", p.handleState)
	mux.HandleFunc("POST /score", p.handleScore)
	mux.HandleFunc("POST /generate", p.handleGenerate)
	mux.HandleFunc("POST /stream", p.handleStream)
	mux.HandleFunc("POST /clear", p.handleClear)
	return mux
}

func (p *probe) Close() {
	for _, stream := range p.streams {
		stream.Close()
	}
	p.eventPool.Shutdown(context.Background())
}

func (p *probe) handleState(writer http.ResponseWriter, _ *http.Request) {
	snapshots := make([]streamSnapshot, 0, len(p.streams))
	for _, id := range p.backendIDs() {
		snapshots = append(snapshots, p.streams[id].Snapshot())
	}

	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	writeJSON(writer, http.StatusOK, stateResponse{
		Streams:       snapshots,
		HeapAllocByte: memory.HeapAlloc,
		HeapSysByte:   memory.HeapSys,
	})
}

func (p *probe) handleScore(writer http.ResponseWriter, request *http.Request) {
	envelope, backend, ok := p.decodeEnvelope(writer, request)
	if !ok {
		return
	}

	tokens, renderLatency, err := p.vllmClient.Render(request.Context(), backend.HTTPURL, envelope.Request)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err)
		return
	}

	podIDs := p.backendIDs()
	lookupStarted := time.Now()
	scores, err := p.indexer.ScoreTokens(request.Context(), tokens, renderedModel(envelope.Request), podIDs, nil)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("查询上游 KV 索引: %w", err))
		return
	}

	matches := make([]matchResult, 0, len(p.backends))
	for _, id := range podIDs {
		snapshot := p.streams[id].Snapshot()
		blocks := scores[id]
		matches = append(matches, matchResult{
			Backend:       id,
			MatchedBlocks: blocks,
			MatchedTokens: int(math.Round(blocks * canonicalBlock)),
			Valid:         snapshot.Valid,
			InvalidReason: snapshot.InvalidReason,
		})
	}
	writeJSON(writer, http.StatusOK, scoreResponse{
		PromptTokens:    len(tokens),
		RenderLatencyMS: milliseconds(renderLatency),
		LookupLatencyMS: milliseconds(time.Since(lookupStarted)),
		Matches:         matches,
	})
}

func (p *probe) handleGenerate(writer http.ResponseWriter, request *http.Request) {
	envelope, backend, ok := p.decodeEnvelope(writer, request)
	if !ok {
		return
	}

	status, body, latency, err := p.vllmClient.Generate(request.Context(), backend.HTTPURL, envelope.Request)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"upstream_status": status,
		"latency_ms":      milliseconds(latency),
		"response":        json.RawMessage(body),
	})
}

func (p *probe) handleStream(writer http.ResponseWriter, request *http.Request) {
	var control streamControl
	if !decodeJSON(writer, request, &control) {
		return
	}
	stream, exists := p.streams[control.Backend]
	if !exists {
		writeError(writer, http.StatusNotFound, fmt.Errorf("未知 backend: %s", control.Backend))
		return
	}
	stream.SetEnabled(control.Enabled)
	writeJSON(writer, http.StatusOK, stream.Snapshot())
}

func (p *probe) handleClear(writer http.ResponseWriter, request *http.Request) {
	var clear clearRequest
	if !decodeJSON(writer, request, &clear) {
		return
	}
	stream, exists := p.streams[clear.Backend]
	if !exists {
		writeError(writer, http.StatusNotFound, fmt.Errorf("未知 backend: %s", clear.Backend))
		return
	}
	if err := p.indexer.KVBlockIndex().Clear(request.Context(), clear.Backend); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("按 Pod 清理索引: %w", err))
		return
	}
	stream.Invalidate("pod-lifecycle-cleared")
	writeJSON(writer, http.StatusOK, stream.Snapshot())
}

func (p *probe) decodeEnvelope(
	writer http.ResponseWriter,
	request *http.Request,
) (requestEnvelope, backendConfig, bool) {
	var envelope requestEnvelope
	if !decodeJSON(writer, request, &envelope) {
		return requestEnvelope{}, backendConfig{}, false
	}
	backend, exists := p.backends[envelope.Backend]
	if !exists {
		writeError(writer, http.StatusNotFound, fmt.Errorf("未知 backend: %s", envelope.Backend))
		return requestEnvelope{}, backendConfig{}, false
	}
	if len(envelope.Request) == 0 {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("request 不能为空"))
		return requestEnvelope{}, backendConfig{}, false
	}
	return envelope, backend, true
}

func (p *probe) backendIDs() []string {
	ids := make([]string, 0, len(p.backends))
	for id := range p.backends {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
