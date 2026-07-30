package endpoint

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/platform/kubernetes"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

const (
	defaultEndpointRefreshInterval = 30 * time.Second
	minimumEndpointWatchTimeout    = 10 * time.Second
	maxEndpointSliceResponseBytes  = 4 << 20
	maxEndpointWatchLineBytes      = 1 << 20
)

// EndpointSliceConfig describes a namespace-scoped discovery client. The
// resolver uses the Kubernetes REST API directly so the serving binary stays
// small and its routing policy remains independent of client-go types.
type EndpointSliceConfig struct {
	Namespace       string
	ServiceName     string
	BaseURL         string
	TokenFile       string
	CAFile          string
	HTTPClient      *http.Client
	RefreshInterval time.Duration
}

type endpointSliceResolver struct {
	config      EndpointSliceConfig
	client      *http.Client
	cancel      context.CancelFunc
	done        chan struct{}
	refreshDone chan struct{}

	mu       sync.RWMutex
	version  string
	slices   map[string]endpointSliceResource
	backends []routing.Backend
	status   ResolverStatus
	closeOne sync.Once
}

// NewEndpointSlice creates a resolver, verifies the initial API snapshot, and
// starts a reconnecting watch. A startup error is returned when the API is not
// reachable or the Service has no usable Ready endpoint.
func NewEndpointSlice(config EndpointSliceConfig) (Resolver, error) {
	if strings.TrimSpace(config.Namespace) == "" {
		return nil, fmt.Errorf("EndpointSlice namespace must not be empty")
	}
	if strings.TrimSpace(config.ServiceName) == "" {
		return nil, fmt.Errorf("EndpointSlice service name must not be empty")
	}
	if strings.TrimSpace(config.TokenFile) == "" {
		return nil, fmt.Errorf("EndpointSlice token file must not be empty")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = inClusterAPIURL()
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("EndpointSlice API URL must be an absolute HTTPS URL: %q", baseURL)
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = defaultEndpointRefreshInterval
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	if config.CAFile != "" && config.HTTPClient == nil {
		client, err = kubernetes.NewHTTPClient(client, config.CAFile)
		if err != nil {
			return nil, err
		}
	}
	// Bound the HTTP client's total watch lifetime as a second line of defense
	// for transports that do not promptly propagate context cancellation from a
	// long-lived streaming response.
	clientCopy := *client
	clientCopy.Timeout = 2 * config.RefreshInterval
	if clientCopy.Timeout < minimumEndpointWatchTimeout {
		clientCopy.Timeout = minimumEndpointWatchTimeout
	}
	client = &clientCopy
	resolver := &endpointSliceResolver{config: config, client: client, done: make(chan struct{}), refreshDone: make(chan struct{}), slices: make(map[string]endpointSliceResource), status: ResolverStatus{Status: routing.ObservationUnavailable}}
	resolver.config.BaseURL = baseURL
	ctx, cancel := context.WithCancel(context.Background())
	resolver.cancel = cancel
	if err := resolver.refresh(ctx); err != nil {
		cancel()
		return nil, err
	}
	go resolver.watchLoop(ctx)
	go resolver.refreshLoop(ctx)
	return resolver, nil
}

func (r *endpointSliceResolver) Snapshot(context.Context) ([]routing.Backend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]routing.Backend(nil), r.backends...), nil
}

func (r *endpointSliceResolver) Status() ResolverStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	status := r.status
	if !status.LastSuccess.IsZero() {
		status.Freshness = time.Since(status.LastSuccess)
	}
	if status.Status == routing.ObservationOK && status.Freshness > 2*r.config.RefreshInterval {
		status.Status = routing.ObservationDegraded
		if status.LastError == "" {
			status.LastError = "EndpointSlice snapshot is stale"
		}
	}
	return status
}

func (r *endpointSliceResolver) Close() error {
	r.closeOne.Do(func() {
		r.cancel()
		<-r.done
		<-r.refreshDone
	})
	return nil
}

// refreshLoop is a bounded relist safety net. Kubernetes watch streams can
// remain open without events even after permissions or API connectivity
// change; periodic relist makes recovery and stale detection deterministic.
func (r *endpointSliceResolver) refreshLoop(ctx context.Context) {
	defer close(r.refreshDone)
	ticker := time.NewTicker(r.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.refresh(ctx)
		}
	}
}

func (r *endpointSliceResolver) watchLoop(ctx context.Context) {
	defer close(r.done)
	for {
		version := r.resourceVersion()
		watchContext, cancel := context.WithTimeout(ctx, 2*r.config.RefreshInterval)
		err := r.watch(watchContext, version)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			r.recordError("EndpointSlice watch stream closed")
		}
		if err != nil {
			r.recordError(err.Error())
			// A stale resourceVersion or a closed stream is recovered by a full
			// list. Backoff prevents an API error from becoming a tight loop.
			_ = r.refresh(ctx)
		}
		timer := time.NewTimer(r.config.RefreshInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *endpointSliceResolver) refresh(ctx context.Context) error {
	var list endpointSliceList
	if err := r.getJSON(ctx, r.listURL(), &list); err != nil {
		r.recordError(err.Error())
		return err
	}
	backends := buildBackends(list.Items)
	if len(backends) == 0 {
		err := fmt.Errorf("service %s/%s has no Ready EndpointSlice addresses", r.config.Namespace, r.config.ServiceName)
		r.recordError(err.Error())
		return err
	}
	r.mu.Lock()
	r.version = list.Metadata.ResourceVersion
	r.slices = make(map[string]endpointSliceResource, len(list.Items))
	for _, item := range list.Items {
		r.slices[item.Metadata.Name] = item
	}
	r.backends = backends
	r.status = ResolverStatus{Status: routing.ObservationOK, LastSuccess: time.Now(), ReadyBackends: len(backends), ResourceVersion: r.version}
	r.mu.Unlock()
	return nil
}

func (r *endpointSliceResolver) recordError(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Status = routing.ObservationDegraded
	if r.status.LastSuccess.IsZero() {
		r.status.Status = routing.ObservationUnavailable
	}
	r.status.LastError = message
	r.status.ReadyBackends = len(r.backends)
	r.status.ResourceVersion = r.version
}

func (r *endpointSliceResolver) watch(ctx context.Context, resourceVersion string) error {
	requestURL := r.listURL()
	query := requestURL.Query()
	query.Set("watch", "true")
	if resourceVersion != "" {
		query.Set("resourceVersion", resourceVersion)
	}
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	if err := r.authorize(request); err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("EndpointSlice watch API returned %s", response.Status)
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, maxEndpointSliceResponseBytes))
	scanner.Buffer(make([]byte, 64*1024), maxEndpointWatchLineBytes)
	for scanner.Scan() {
		var event endpointSliceWatchEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode EndpointSlice watch event: %w", err)
		}
		if event.Type == "ERROR" {
			return fmt.Errorf("EndpointSlice watch returned an error event")
		}
		if event.Object.Metadata.Name == "" {
			continue
		}
		r.mu.Lock()
		switch event.Type {
		case "ADDED", "MODIFIED":
			r.slices[event.Object.Metadata.Name] = event.Object
		case "DELETED":
			delete(r.slices, event.Object.Metadata.Name)
		default:
			r.mu.Unlock()
			continue
		}
		r.version = event.Object.Metadata.ResourceVersion
		r.backends = buildBackendsFromMap(r.slices)
		r.mu.Unlock()
	}
	return scanner.Err()
}

func (r *endpointSliceResolver) listURL() *url.URL {
	base, _ := url.Parse(r.config.BaseURL)
	base.Path = strings.TrimRight(base.Path, "/") + "/apis/discovery.k8s.io/v1/namespaces/" + url.PathEscape(r.config.Namespace) + "/endpointslices"
	query := base.Query()
	query.Set("labelSelector", "kubernetes.io/service-name="+r.config.ServiceName)
	base.RawQuery = query.Encode()
	return base
}

func (r *endpointSliceResolver) getJSON(ctx context.Context, requestURL *url.URL, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	if err := r.authorize(request); err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("EndpointSlice API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxEndpointSliceResponseBytes)).Decode(destination); err != nil {
		return fmt.Errorf("decode EndpointSlice API response: %w", err)
	}
	return nil
}

func (r *endpointSliceResolver) authorize(request *http.Request) error {
	token, err := os.ReadFile(r.config.TokenFile)
	if err != nil {
		return fmt.Errorf("read Kubernetes service account token: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	return nil
}

func (r *endpointSliceResolver) resourceVersion() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

func buildBackends(items []endpointSliceResource) []routing.Backend {
	itemsByName := make(map[string]endpointSliceResource, len(items))
	for index, item := range items {
		key := item.Metadata.Name
		if key == "" {
			key = fmt.Sprintf("item-%d", index)
		}
		itemsByName[key] = item
	}
	return buildBackendsFromMap(itemsByName)
}

func buildBackendsFromMap(items map[string]endpointSliceResource) []routing.Backend {
	type addressPort struct {
		address string
		port    int32
		podName string
	}
	unique := make(map[addressPort]struct{})
	for _, item := range items {
		if item.AddressType != "IPv4" && item.AddressType != "IPv6" {
			continue
		}
		port, ok := endpointPort(item.Ports)
		if !ok {
			continue
		}
		for _, endpoint := range item.Endpoints {
			if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready {
				continue
			}
			for _, address := range endpoint.Addresses {
				if strings.TrimSpace(address) != "" {
					podName := ""
					if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" {
						podName = endpoint.TargetRef.Name
					}
					unique[addressPort{address: address, port: port, podName: podName}] = struct{}{}
				}
			}
		}
	}
	keys := make([]addressPort, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].address == keys[j].address {
			return keys[i].port < keys[j].port
		}
		return keys[i].address < keys[j].address
	})
	backends := make([]routing.Backend, 0, len(keys))
	for _, key := range keys {
		host := net.JoinHostPort(key.address, strconv.Itoa(int(key.port)))
		sum := sha256.Sum256([]byte(host))
		metadata := map[string]string{}
		if key.podName != "" {
			metadata["pod_name"] = key.podName
		}
		backends = append(backends, routing.Backend{ID: "endpoint-" + hex.EncodeToString(sum[:6]), URL: "http://" + host, Metadata: metadata})
	}
	return backends
}

func endpointPort(ports []endpointSlicePort) (int32, bool) {
	var first int32
	for _, port := range ports {
		if port.Port == nil || *port.Port <= 0 {
			continue
		}
		if first == 0 {
			first = *port.Port
		}
		if port.Name != nil && *port.Name == "http" {
			return *port.Port, true
		}
	}
	return first, first > 0
}

func inClusterAPIURL() string {
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	if host == "" {
		return ""
	}
	port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"))
	if port == "" {
		port = "443"
	}
	return "https://" + net.JoinHostPort(host, port)
}

type endpointSliceList struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Items []endpointSliceResource `json:"items"`
}

type endpointSliceWatchEvent struct {
	Type   string                `json:"type"`
	Object endpointSliceResource `json:"object"`
}

type endpointSliceResource struct {
	Metadata struct {
		Name            string `json:"name"`
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	AddressType string              `json:"addressType"`
	Ports       []endpointSlicePort `json:"ports"`
	Endpoints   []endpointEntry     `json:"endpoints"`
}

type endpointSlicePort struct {
	Name     *string `json:"name"`
	Port     *int32  `json:"port"`
	Protocol *string `json:"protocol"`
}

type endpointEntry struct {
	Addresses  []string           `json:"addresses"`
	TargetRef  *endpointTargetRef `json:"targetRef"`
	Conditions struct {
		Ready       *bool `json:"ready"`
		Serving     *bool `json:"serving"`
		Terminating *bool `json:"terminating"`
	} `json:"conditions"`
}

type endpointTargetRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}
