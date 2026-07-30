// Package identity maps serving backends to Kubernetes workload identity.
// It intentionally stops at Pod -> Node and declared GPU request; live GPU
// utilization remains a separate node-level telemetry concern.
package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/frozenf1sh/fishmesh/internal/platform/kubernetes"
	"github.com/frozenf1sh/fishmesh/internal/serving/routing"
)

const maxResponseBytes = 4 << 20

type Config struct {
	Namespace  string
	BaseURL    string
	TokenFile  string
	CAFile     string
	HTTPClient *http.Client
}

// Provider lists Pods once per observation refresh and maps EndpointSlice
// targetRef metadata to Pod status, node name and declared GPU request.
type Provider struct {
	namespace string
	baseURL   string
	tokenFile string
	client    *http.Client
}

func New(config Config) (*Provider, error) {
	if strings.TrimSpace(config.Namespace) == "" {
		return nil, fmt.Errorf("identity namespace must not be empty")
	}
	if strings.TrimSpace(config.TokenFile) == "" {
		return nil, fmt.Errorf("identity token file must not be empty")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = inClusterAPIURL()
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("identity API URL must be an absolute HTTPS URL: %q", baseURL)
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
		if config.CAFile != "" {
			client, err = kubernetes.NewHTTPClient(client, config.CAFile)
			if err != nil {
				return nil, err
			}
		}
	}
	return &Provider{namespace: config.Namespace, baseURL: baseURL, tokenFile: config.TokenFile, client: client}, nil
}

func (p *Provider) Enrich(ctx context.Context, backends []routing.Backend) (map[string]routing.BackendIdentity, error) {
	var pods podList
	if err := p.getJSON(ctx, "/api/v1/namespaces/"+url.PathEscape(p.namespace)+"/pods", &pods); err != nil {
		return nil, err
	}
	byName := make(map[string]pod, len(pods.Items))
	for _, item := range pods.Items {
		byName[item.Metadata.Name] = item
	}
	identities := make(map[string]routing.BackendIdentity, len(backends))
	for _, backend := range backends {
		podName := backend.Metadata["pod_name"]
		identity := routing.BackendIdentity{PodName: podName, Status: routing.ObservationUnavailable}
		if podName == "" {
			identity.Error = "EndpointSlice backend has no Pod targetRef"
			identities[backend.ID] = identity
			continue
		}
		item, ok := byName[podName]
		if !ok {
			identity.Error = "Pod targetRef is not present in namespace snapshot"
			identities[backend.ID] = identity
			continue
		}
		identity.NodeName = item.Spec.NodeName
		identity.GPURequested = gpuRequested(item.Spec.Containers)
		identity.Ready = item.Ready()
		identity.Status = routing.ObservationOK
		if !identity.Ready {
			identity.Status = routing.ObservationDegraded
			identity.Error = "Pod is not Ready"
		}
		identities[backend.ID] = identity
	}
	return identities, nil
}

func (p *Provider) getJSON(ctx context.Context, requestPath string, destination any) error {
	token, err := os.ReadFile(p.tokenFile)
	if err != nil {
		return fmt.Errorf("read Kubernetes service account token: %w", err)
	}
	requestURL := strings.TrimRight(p.baseURL, "/") + path.Join("/", requestPath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("Kubernetes Pod API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(destination); err != nil {
		return fmt.Errorf("decode Kubernetes Pod API response: %w", err)
	}
	return nil
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

type podList struct {
	Items []pod `json:"items"`
}

type pod struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string      `json:"nodeName"`
		Containers []container `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase      string      `json:"phase"`
		Conditions []condition `json:"conditions"`
	} `json:"status"`
}

type container struct {
	Resources resources `json:"resources"`
}

type resources struct {
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

func (p pod) Ready() bool {
	if p.Status.Phase != "Running" {
		return false
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return false
}

func gpuRequested(containers []container) float64 {
	var total float64
	for _, container := range containers {
		value := container.Resources.Requests["nvidia.com/gpu"]
		if value == "" {
			value = container.Resources.Limits["nvidia.com/gpu"]
		}
		if value == "" {
			value = container.Resources.Requests["amd.com/gpu"]
		}
		if value == "" {
			value = container.Resources.Limits["amd.com/gpu"]
		}
		total += parseQuantity(value)
	}
	return total
}

func parseQuantity(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if strings.HasSuffix(value, "m") {
		parsed, err := strconv.ParseFloat(strings.TrimSuffix(value, "m"), 64)
		if err != nil {
			return 0
		}
		return parsed / 1000
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}
