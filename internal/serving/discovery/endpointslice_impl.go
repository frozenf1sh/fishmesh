package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/platform/kubernetes"
	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	defaultEndpointRefreshInterval = 30 * time.Second
	minimumEndpointWatchTimeout    = 10 * time.Second
	endpointSnapshotStaleError     = "EndpointSlice snapshot is stale"
	endpointWatchClosedError       = "EndpointSlice watch stream closed"
	secureURLScheme                = "https"
)

var _ Resolver = (*endpointSliceResolver)(nil)

type endpointSliceResolver struct {
	config      EndpointSliceConfig
	client      *http.Client
	cancel      context.CancelFunc
	done        chan struct{}
	refreshDone chan struct{}

	mu       sync.RWMutex
	version  string
	slices   map[string]endpointSliceResource
	backends []backend.Backend
	status   ResolverStatus
	closeOne sync.Once
}

// NewEndpointSlice creates a resolver, verifies the initial API snapshot, and
// starts a reconnecting watch. Startup fails when no Ready endpoint exists.
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
	if err != nil || parsed.Scheme != secureURLScheme || parsed.Host == "" {
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
	clientCopy := *client
	clientCopy.Timeout = 2 * config.RefreshInterval
	if clientCopy.Timeout < minimumEndpointWatchTimeout {
		clientCopy.Timeout = minimumEndpointWatchTimeout
	}

	resolver := &endpointSliceResolver{
		config: config, client: &clientCopy, done: make(chan struct{}),
		refreshDone: make(chan struct{}), slices: make(map[string]endpointSliceResource),
		status: ResolverStatus{Status: StatusUnavailable},
	}
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

func (r *endpointSliceResolver) Snapshot(context.Context) ([]backend.Backend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]backend.Backend(nil), r.backends...), nil
}

func (r *endpointSliceResolver) Status() ResolverStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	status := r.status
	if !status.LastSuccess.IsZero() {
		status.Freshness = time.Since(status.LastSuccess)
	}
	if status.Status == StatusOK && status.Freshness > 2*r.config.RefreshInterval {
		status.Status = StatusDegraded
		if status.LastError == "" {
			status.LastError = endpointSnapshotStaleError
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
		watchContext, cancel := context.WithTimeout(ctx, 2*r.config.RefreshInterval)
		err := r.watch(watchContext, r.resourceVersion())
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			r.recordError(endpointWatchClosedError)
		} else {
			r.recordError(err.Error())
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
	r.status = ResolverStatus{Status: StatusOK, LastSuccess: time.Now(), ReadyBackends: len(backends), ResourceVersion: r.version}
	r.mu.Unlock()
	return nil
}

func (r *endpointSliceResolver) recordError(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Status = StatusDegraded
	if r.status.LastSuccess.IsZero() {
		r.status.Status = StatusUnavailable
	}
	r.status.LastError = message
	r.status.ReadyBackends = len(r.backends)
	r.status.ResourceVersion = r.version
}

func (r *endpointSliceResolver) resourceVersion() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}
