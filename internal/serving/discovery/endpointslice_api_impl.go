package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	maxEndpointSliceResponseBytes = 4 << 20
	maxEndpointWatchLineBytes     = 1 << 20
	maxAPIErrorBodyBytes          = 1024
	initialScannerBufferBytes     = 64 * 1024

	endpointSliceAPIPathFormat = "/apis/discovery.k8s.io/v1/namespaces/%s/endpointslices"
	serviceNameLabel           = "kubernetes.io/service-name"
	queryLabelSelector         = "labelSelector"
	queryWatch                 = "watch"
	queryResourceVersion       = "resourceVersion"
	mediaTypeJSON              = "application/json"
	headerAccept               = "Accept"
	headerAuthorization        = "Authorization"
	bearerPrefix               = "Bearer "

	watchEventAdded    = "ADDED"
	watchEventModified = "MODIFIED"
	watchEventDeleted  = "DELETED"
	watchEventError    = "ERROR"

	kubernetesServiceHostEnv      = "KUBERNETES_SERVICE_HOST"
	kubernetesServiceHTTPSPortEnv = "KUBERNETES_SERVICE_PORT_HTTPS"
	defaultKubernetesHTTPSPort    = "443"
)

func (r *endpointSliceResolver) watch(ctx context.Context, resourceVersion string) error {
	requestURL := r.listURL()
	query := requestURL.Query()
	query.Set(queryWatch, "true")
	if resourceVersion != "" {
		query.Set(queryResourceVersion, resourceVersion)
	}
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	if err := r.authorize(request); err != nil {
		return err
	}
	request.Header.Set(headerAccept, mediaTypeJSON)
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("EndpointSlice watch API returned %s", response.Status)
	}

	scanner := bufio.NewScanner(io.LimitReader(response.Body, maxEndpointSliceResponseBytes))
	scanner.Buffer(make([]byte, initialScannerBufferBytes), maxEndpointWatchLineBytes)
	for scanner.Scan() {
		var event endpointSliceWatchEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode EndpointSlice watch event: %w", err)
		}
		if event.Type == watchEventError {
			return fmt.Errorf("EndpointSlice watch returned an error event")
		}
		if event.Object.Metadata.Name == "" {
			continue
		}
		r.mu.Lock()
		switch event.Type {
		case watchEventAdded, watchEventModified:
			r.slices[event.Object.Metadata.Name] = event.Object
		case watchEventDeleted:
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
	base.Path = strings.TrimRight(base.Path, "/") + fmt.Sprintf(endpointSliceAPIPathFormat, url.PathEscape(r.config.Namespace))
	query := base.Query()
	query.Set(queryLabelSelector, serviceNameLabel+"="+r.config.ServiceName)
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
	request.Header.Set(headerAccept, mediaTypeJSON)
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxAPIErrorBodyBytes))
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
	request.Header.Set(headerAuthorization, bearerPrefix+strings.TrimSpace(string(token)))
	return nil
}

func inClusterAPIURL() string {
	host := strings.TrimSpace(os.Getenv(kubernetesServiceHostEnv))
	if host == "" {
		return ""
	}
	port := strings.TrimSpace(os.Getenv(kubernetesServiceHTTPSPortEnv))
	if port == "" {
		port = defaultKubernetesHTTPSPort
	}
	return secureURLScheme + "://" + net.JoinHostPort(host, port)
}
