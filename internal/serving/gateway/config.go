package gateway

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddress = ":8080"
	// 默认 upstream 指向 baseline 用的普通 ClusterIP Service（qwen-vllm-baseline）。
	// ClusterIP Service 由 kube-proxy 做随机的连接级 endpoint 选择——这正是
	// random-Service 对照组要测量的行为；K8s 内部 DNS 名带 .svc.cluster.local
	// 后缀，仅集群内可解析。
	defaultUpstreamURL                   = "http://qwen-vllm-baseline.kubellm.svc.cluster.local:8000"
	defaultRoutingMode                   = "service"
	defaultAffinityTTL                   = 5 * time.Minute
	defaultAffinityMaxEntries            = 10_000
	defaultAffinityInflightDelta   int64 = 2
	defaultAffinityQueueDepthDelta       = 1.0
	defaultMaxInflightRequests           = 128
	defaultMaxConnsPerHost               = 32
	defaultCircuitEWMAAlpha              = 0.5
	defaultCircuitErrorThreshold         = 0.6
	defaultCircuitMinimumRequests        = 3
	defaultCircuitOpenDuration           = 10 * time.Second
)

// Config 是 Gateway 的全部运行时设置。配置刻意只走环境变量，原因有两个：
//  1. 同一个镜像在本地（go run）和 Kubernetes（Deployment env）都能运行，
//     无需重新编译；
//  2. 环境变量是 12-factor 的配置方式，与 K8s 的 ConfigMap/Secret 天然集成，
//     不把敏感配置硬编码进镜像。
//
// 所有字段都给了合理默认值，因此零配置也能启动；显式配置通过校验后覆盖默认值。
type Config struct {
	ListenAddress           string        // HTTP 监听地址，如 ":8080"
	UpstreamURL             string        // 上游 vLLM 服务的完整 URL，如上面的 ClusterIP DNS 名
	RoutingMode             string        // service、prefix-affinity 或 load-aware
	BackendEndpoints        []string      // endpoint 路由模式下的实验 backend URL 列表
	EndpointDiscovery       string        // static 或 endpointslice
	EndpointService         string        // EndpointSlice 对应的 Service 名称
	EndpointNamespace       string        // EndpointSlice 所在 namespace
	KubernetesAPIURL        string        // 可选；为空时使用 in-cluster API 地址
	KubernetesToken         string        // ServiceAccount token 文件
	KubernetesCA            string        // ServiceAccount CA 文件
	EndpointRefresh         time.Duration // EndpointSlice watch 重连间隔
	EndpointMaxAge          time.Duration // EndpointSlice 成功快照允许保持 Ready 的最长时间
	ObservationMode         string        // none 或 prometheus
	ObservationInterval     time.Duration // backend metrics 采集间隔
	ObservationMaxAge       time.Duration // 快照允许保持 OK 的最长时间
	KeepAlive               bool          // 是否复用 Gateway 到 upstream 的 HTTP 连接
	RequestTimeout          time.Duration // 单个请求（含流式响应全程）的超时上限
	ShutdownTimeout         time.Duration // 优雅关停时等待 in-flight 请求完成的上限
	AffinityTTL             time.Duration // routing-key preference 的滑动过期时间
	AffinityMaxEntries      int           // registry 的硬上限，防止高基数 key 耗尽内存
	AffinityInflightDelta   int64         // preferred 相对最空闲 backend 允许的 local in-flight 差值
	AffinityQueueDepthDelta float64       // preferred 相对最小 vLLM queue 允许的差值
	MaxInflightRequests     int           // Gateway 同时接收的 /v1 请求硬上限，超过时返回 429
	MaxConnsPerHost         int           // 每个 backend 的 TCP 连接硬上限
	CircuitEWMAAlpha        float64       // transport error EWMA 中最新样本的权重
	CircuitErrorThreshold   float64       // 达到该错误率后临时隔离 backend
	CircuitMinimumRequests  int           // 打开 circuit 前至少需要的完成样本数
	CircuitOpenDuration     time.Duration // backend 被临时隔离的时长
}

// LoadConfigFromEnvironment 从 FISHMESH_* 环境变量读取配置并校验。
// 返回的 Config 保证已通过 Validate()，可以直接安全使用。
func LoadConfigFromEnvironment() (Config, error) {
	endpointRefresh, err := durationFromEnvironment("FISHMESH_ENDPOINT_REFRESH_INTERVAL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	endpointMaxAge, err := durationFromEnvironment("FISHMESH_ENDPOINT_MAX_AGE", 90*time.Second)
	if err != nil {
		return Config{}, err
	}
	observationInterval, err := durationFromEnvironment("FISHMESH_BACKEND_OBSERVATION_INTERVAL", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	observationMaxAge, err := durationFromEnvironment("FISHMESH_BACKEND_OBSERVATION_MAX_AGE", 45*time.Second)
	if err != nil {
		return Config{}, err
	}
	keepAlive, err := boolFromEnvironment("FISHMESH_UPSTREAM_KEEPALIVE", false)
	if err != nil {
		return Config{}, err
	}
	requestTimeout, err := durationFromEnvironment("FISHMESH_REQUEST_TIMEOUT", 90*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := durationFromEnvironment("FISHMESH_SHUTDOWN_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	affinityTTL, err := durationFromEnvironment("FISHMESH_AFFINITY_TTL", defaultAffinityTTL)
	if err != nil {
		return Config{}, err
	}
	affinityMaxEntries, err := intFromEnvironment("FISHMESH_AFFINITY_MAX_ENTRIES", defaultAffinityMaxEntries)
	if err != nil {
		return Config{}, err
	}
	affinityInflightDelta, err := int64FromEnvironment("FISHMESH_AFFINITY_INFLIGHT_DELTA", defaultAffinityInflightDelta)
	if err != nil {
		return Config{}, err
	}
	affinityQueueDepthDelta, err := floatFromEnvironment("FISHMESH_AFFINITY_QUEUE_DEPTH_DELTA", defaultAffinityQueueDepthDelta)
	if err != nil {
		return Config{}, err
	}
	maxInflightRequests, err := positiveIntFromEnvironment("FISHMESH_MAX_INFLIGHT_REQUESTS", defaultMaxInflightRequests)
	if err != nil {
		return Config{}, err
	}
	maxConnsPerHost, err := positiveIntFromEnvironment("FISHMESH_MAX_CONNS_PER_HOST", defaultMaxConnsPerHost)
	if err != nil {
		return Config{}, err
	}
	circuitEWMAAlpha, err := floatFromEnvironment("FISHMESH_CIRCUIT_EWMA_ALPHA", defaultCircuitEWMAAlpha)
	if err != nil {
		return Config{}, err
	}
	if circuitEWMAAlpha <= 0 || circuitEWMAAlpha > 1 {
		return Config{}, fmt.Errorf("FISHMESH_CIRCUIT_EWMA_ALPHA must be in (0, 1]: %g", circuitEWMAAlpha)
	}
	circuitErrorThreshold, err := floatFromEnvironment("FISHMESH_CIRCUIT_ERROR_THRESHOLD", defaultCircuitErrorThreshold)
	if err != nil {
		return Config{}, err
	}
	if circuitErrorThreshold <= 0 || circuitErrorThreshold > 1 {
		return Config{}, fmt.Errorf("FISHMESH_CIRCUIT_ERROR_THRESHOLD must be in (0, 1]: %g", circuitErrorThreshold)
	}
	circuitMinimumRequests, err := positiveIntFromEnvironment("FISHMESH_CIRCUIT_MIN_REQUESTS", defaultCircuitMinimumRequests)
	if err != nil {
		return Config{}, err
	}
	circuitOpenDuration, err := durationFromEnvironment("FISHMESH_CIRCUIT_OPEN_DURATION", defaultCircuitOpenDuration)
	if err != nil {
		return Config{}, err
	}
	if circuitOpenDuration <= 0 {
		return Config{}, fmt.Errorf("FISHMESH_CIRCUIT_OPEN_DURATION must be positive: %s", circuitOpenDuration)
	}
	config := Config{
		ListenAddress:           valueOrDefault("FISHMESH_LISTEN_ADDRESS", defaultListenAddress),
		UpstreamURL:             valueOrDefault("FISHMESH_UPSTREAM_URL", defaultUpstreamURL),
		RoutingMode:             valueOrDefault("FISHMESH_ROUTING_MODE", defaultRoutingMode),
		BackendEndpoints:        backendEndpointsFromEnvironment(),
		EndpointDiscovery:       valueOrDefault("FISHMESH_ENDPOINT_DISCOVERY", "static"),
		EndpointService:         valueOrDefault("FISHMESH_ENDPOINT_SERVICE", "qwen-vllm"),
		EndpointNamespace:       valueOrDefault("FISHMESH_ENDPOINT_NAMESPACE", "kubellm"),
		KubernetesAPIURL:        strings.TrimSpace(os.Getenv("FISHMESH_KUBERNETES_API_URL")),
		KubernetesToken:         valueOrDefault("FISHMESH_KUBERNETES_TOKEN_FILE", "/var/run/secrets/kubernetes.io/serviceaccount/token"),
		KubernetesCA:            valueOrDefault("FISHMESH_KUBERNETES_CA_FILE", "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"),
		EndpointRefresh:         endpointRefresh,
		EndpointMaxAge:          endpointMaxAge,
		ObservationMode:         valueOrDefault("FISHMESH_BACKEND_OBSERVATION_MODE", "none"),
		ObservationInterval:     observationInterval,
		ObservationMaxAge:       observationMaxAge,
		KeepAlive:               keepAlive,
		RequestTimeout:          requestTimeout,
		ShutdownTimeout:         shutdownTimeout,
		AffinityTTL:             affinityTTL,
		AffinityMaxEntries:      affinityMaxEntries,
		AffinityInflightDelta:   affinityInflightDelta,
		AffinityQueueDepthDelta: affinityQueueDepthDelta,
		MaxInflightRequests:     maxInflightRequests,
		MaxConnsPerHost:         maxConnsPerHost,
		CircuitEWMAAlpha:        circuitEWMAAlpha,
		CircuitErrorThreshold:   circuitErrorThreshold,
		CircuitMinimumRequests:  circuitMinimumRequests,
		CircuitOpenDuration:     circuitOpenDuration,
	}
	return config, config.Validate()
}

// Validate 检查配置是否可运行。设计原则是"启动时 loud fail"：
// 配置错误应该在进程启动的第一时间暴露，而不是等到第一个请求失败时
// 才让人在日志里翻找原因。
func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddress) == "" {
		return fmt.Errorf("listen address must not be empty")
	}
	upstream, err := url.Parse(c.UpstreamURL)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return fmt.Errorf("upstream URL must be an absolute HTTP URL: %q", c.UpstreamURL)
	}
	// 只允许 http/https：网关是纯代理，不引入其他协议来增加攻击面。
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return fmt.Errorf("upstream URL scheme must be http or https: %q", c.UpstreamURL)
	}
	if c.RequestTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return fmt.Errorf("timeouts must be positive")
	}
	if c.MaxInflightRequests < 0 || c.MaxConnsPerHost < 0 || c.CircuitMinimumRequests < 0 || c.CircuitOpenDuration < 0 {
		return fmt.Errorf("reliability limits must not be negative")
	}
	if c.CircuitEWMAAlpha < 0 || c.CircuitEWMAAlpha > 1 || c.CircuitErrorThreshold < 0 || c.CircuitErrorThreshold > 1 {
		return fmt.Errorf("circuit EWMA alpha and error threshold must be in (0, 1]")
	}
	if c.RoutingMode == "" {
		// Preserve zero-value Config behavior used by unit tests and local callers.
		return nil
	}
	if c.EndpointDiscovery != "" && c.EndpointDiscovery != "static" && c.EndpointDiscovery != "endpointslice" {
		return fmt.Errorf("endpoint discovery must be static or endpointslice: %q", c.EndpointDiscovery)
	}
	if c.EndpointDiscovery == "endpointslice" && c.EndpointRefresh <= 0 {
		return fmt.Errorf("endpoint refresh interval must be positive")
	}
	if c.EndpointDiscovery == "endpointslice" && c.EndpointMaxAge <= 0 {
		return fmt.Errorf("endpoint max age must be positive")
	}
	if c.ObservationMode != "" && c.ObservationMode != "none" && c.ObservationMode != "prometheus" {
		return fmt.Errorf("backend observation mode must be none or prometheus: %q", c.ObservationMode)
	}
	if c.ObservationMode == "prometheus" && (c.ObservationInterval <= 0 || c.ObservationMaxAge <= 0) {
		return fmt.Errorf("backend observation interval and max age must be positive")
	}
	if c.RoutingMode != "service" && c.RoutingMode != "prefix-hash" && c.RoutingMode != "prefix-affinity" && c.RoutingMode != "load-aware" && c.RoutingMode != "bounded-affinity" {
		return fmt.Errorf("routing mode must be service, prefix-affinity, load-aware, or bounded-affinity: %q", c.RoutingMode)
	}
	if c.RoutingMode == "bounded-affinity" {
		if c.AffinityTTL <= 0 || c.AffinityMaxEntries <= 0 {
			return fmt.Errorf("bounded affinity TTL and max entries must be positive")
		}
		if c.AffinityInflightDelta < 0 || c.AffinityQueueDepthDelta < 0 {
			return fmt.Errorf("bounded affinity deltas must not be negative")
		}
	}
	if c.EndpointDiscovery == "endpointslice" {
		if strings.TrimSpace(c.EndpointService) == "" || strings.TrimSpace(c.EndpointNamespace) == "" {
			return fmt.Errorf("EndpointSlice discovery requires service and namespace")
		}
	}
	if c.EndpointDiscovery == "static" && (c.RoutingMode == "prefix-hash" || c.RoutingMode == "prefix-affinity" || c.RoutingMode == "load-aware" || c.RoutingMode == "bounded-affinity") {
		if len(c.BackendEndpoints) < 1 {
			return fmt.Errorf("%s routing requires at least one backend endpoint", c.RoutingMode)
		}
		for _, endpoint := range c.BackendEndpoints {
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("backend endpoint must be an absolute HTTP URL: %q", endpoint)
			}
		}
	}
	return nil
}

// withReliabilityDefaults keeps direct unit-test Config literals concise while
// environment parsing still rejects explicitly configured zero limits.
func (c Config) withReliabilityDefaults() Config {
	if c.MaxInflightRequests == 0 {
		c.MaxInflightRequests = defaultMaxInflightRequests
	}
	if c.MaxConnsPerHost == 0 {
		c.MaxConnsPerHost = defaultMaxConnsPerHost
	}
	if c.CircuitEWMAAlpha == 0 {
		c.CircuitEWMAAlpha = defaultCircuitEWMAAlpha
	}
	if c.CircuitErrorThreshold == 0 {
		c.CircuitErrorThreshold = defaultCircuitErrorThreshold
	}
	if c.CircuitMinimumRequests == 0 {
		c.CircuitMinimumRequests = defaultCircuitMinimumRequests
	}
	if c.CircuitOpenDuration == 0 {
		c.CircuitOpenDuration = defaultCircuitOpenDuration
	}
	return c
}

// valueOrDefault 读取环境变量；为空时回退到默认值。
func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// durationFromEnvironment only uses the fallback when the variable is absent.
// An explicitly configured but malformed value is a deployment error and must
// fail startup instead of silently changing scheduler or timeout semantics.
func durationFromEnvironment(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %q: %w", key, value, err)
	}
	return parsed, nil
}

func boolFromEnvironment(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %q: %w", key, value, err)
	}
	return parsed, nil
}

func intFromEnvironment(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %q: %w", key, value, err)
	}
	return parsed, nil
}

func positiveIntFromEnvironment(key string, fallback int) (int, error) {
	value, err := intFromEnvironment(key, fallback)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive: %d", key, value)
	}
	return value, nil
}

func int64FromEnvironment(key string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %q: %w", key, value, err)
	}
	return parsed, nil
}

func floatFromEnvironment(key string, fallback float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %q: %w", key, value, err)
	}
	return parsed, nil
}

func csvValues(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func backendEndpointsFromEnvironment() []string {
	// FISHMESH_BACKEND_ENDPOINTS is the new name. The old prefix-specific name
	// remains a read-only compatibility alias for existing experiment overlays.
	value := os.Getenv("FISHMESH_BACKEND_ENDPOINTS")
	if strings.TrimSpace(value) == "" {
		value = os.Getenv("FISHMESH_PREFIX_ENDPOINTS")
	}
	return csvValues(value)
}
