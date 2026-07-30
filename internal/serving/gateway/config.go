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
	defaultUpstreamURL = "http://qwen-vllm-baseline.kubellm.svc.cluster.local:8000"
	defaultRoutingMode = "service"
)

// Config 是 Gateway 的全部运行时设置。配置刻意只走环境变量，原因有两个：
//  1. 同一个镜像在本地（go run）和 Kubernetes（Deployment env）都能运行，
//     无需重新编译；
//  2. 环境变量是 12-factor 的配置方式，与 K8s 的 ConfigMap/Secret 天然集成，
//     不把敏感配置硬编码进镜像。
//
// 所有字段都给了合理默认值，因此零配置也能启动；显式配置通过校验后覆盖默认值。
type Config struct {
	ListenAddress       string        // HTTP 监听地址，如 ":8080"
	UpstreamURL         string        // 上游 vLLM 服务的完整 URL，如上面的 ClusterIP DNS 名
	RoutingMode         string        // service、prefix-affinity 或 load-aware
	BackendEndpoints    []string      // endpoint 路由模式下的实验 backend URL 列表
	EndpointDiscovery   string        // static 或 endpointslice
	EndpointService     string        // EndpointSlice 对应的 Service 名称
	EndpointNamespace   string        // EndpointSlice 所在 namespace
	KubernetesAPIURL    string        // 可选；为空时使用 in-cluster API 地址
	KubernetesToken     string        // ServiceAccount token 文件
	KubernetesCA        string        // ServiceAccount CA 文件
	EndpointRefresh     time.Duration // EndpointSlice watch 重连间隔
	EndpointMaxAge      time.Duration // EndpointSlice 成功快照允许保持 Ready 的最长时间
	ObservationMode     string        // none 或 prometheus
	ObservationInterval time.Duration // backend metrics 采集间隔
	ObservationMaxAge   time.Duration // 快照允许保持 OK 的最长时间
	KeepAlive           bool          // 是否复用 Gateway 到 upstream 的 HTTP 连接
	RequestTimeout      time.Duration // 单个请求（含流式响应全程）的超时上限
	ShutdownTimeout     time.Duration // 优雅关停时等待 in-flight 请求完成的上限
}

// LoadConfigFromEnvironment 从 FISHMESH_* 环境变量读取配置并校验。
// 返回的 Config 保证已通过 Validate()，可以直接安全使用。
func LoadConfigFromEnvironment() (Config, error) {
	config := Config{
		ListenAddress:       valueOrDefault("FISHMESH_LISTEN_ADDRESS", defaultListenAddress),
		UpstreamURL:         valueOrDefault("FISHMESH_UPSTREAM_URL", defaultUpstreamURL),
		RoutingMode:         valueOrDefault("FISHMESH_ROUTING_MODE", defaultRoutingMode),
		BackendEndpoints:    backendEndpointsFromEnvironment(),
		EndpointDiscovery:   valueOrDefault("FISHMESH_ENDPOINT_DISCOVERY", "static"),
		EndpointService:     valueOrDefault("FISHMESH_ENDPOINT_SERVICE", "qwen-vllm"),
		EndpointNamespace:   valueOrDefault("FISHMESH_ENDPOINT_NAMESPACE", "kubellm"),
		KubernetesAPIURL:    strings.TrimSpace(os.Getenv("FISHMESH_KUBERNETES_API_URL")),
		KubernetesToken:     valueOrDefault("FISHMESH_KUBERNETES_TOKEN_FILE", "/var/run/secrets/kubernetes.io/serviceaccount/token"),
		KubernetesCA:        valueOrDefault("FISHMESH_KUBERNETES_CA_FILE", "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"),
		EndpointRefresh:     durationOrDefault("FISHMESH_ENDPOINT_REFRESH_INTERVAL", 30*time.Second),
		EndpointMaxAge:      durationOrDefault("FISHMESH_ENDPOINT_MAX_AGE", 90*time.Second),
		ObservationMode:     valueOrDefault("FISHMESH_BACKEND_OBSERVATION_MODE", "none"),
		ObservationInterval: durationOrDefault("FISHMESH_BACKEND_OBSERVATION_INTERVAL", 15*time.Second),
		ObservationMaxAge:   durationOrDefault("FISHMESH_BACKEND_OBSERVATION_MAX_AGE", 45*time.Second),
		KeepAlive:           boolOrDefault("FISHMESH_UPSTREAM_KEEPALIVE", false),
		RequestTimeout:      durationOrDefault("FISHMESH_REQUEST_TIMEOUT", 90*time.Second),
		ShutdownTimeout:     durationOrDefault("FISHMESH_SHUTDOWN_TIMEOUT", 30*time.Second),
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
	if c.RoutingMode != "service" && c.RoutingMode != "prefix-hash" && c.RoutingMode != "prefix-affinity" && c.RoutingMode != "load-aware" {
		return fmt.Errorf("routing mode must be service, prefix-affinity, or load-aware: %q", c.RoutingMode)
	}
	if c.EndpointDiscovery == "endpointslice" {
		if strings.TrimSpace(c.EndpointService) == "" || strings.TrimSpace(c.EndpointNamespace) == "" {
			return fmt.Errorf("EndpointSlice discovery requires service and namespace")
		}
	}
	if c.EndpointDiscovery == "static" && (c.RoutingMode == "prefix-hash" || c.RoutingMode == "prefix-affinity" || c.RoutingMode == "load-aware") {
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

// valueOrDefault 读取环境变量；为空时回退到默认值。
func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// durationOrDefault 读取并解析 duration 类型环境变量（如 "90s"、"5m"）。
// 与 valueOrDefault 不同，这里解析失败也是静默回退默认值，而不是报错：
// 超时属于"可容忍的笔误"级别的问题，不值得让整个进程拒绝启动；
// 真正致命的问题由 Validate() 负责拦截。
func durationOrDefault(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func boolOrDefault(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
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
