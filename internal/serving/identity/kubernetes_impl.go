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
	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	maxResponseBytes     = 4 << 20
	maxAPIErrorBodyBytes = 1024
	headerAccept         = "Accept"
	headerAuthorization  = "Authorization"
	mediaTypeJSON        = "application/json"
	bearerPrefix         = "Bearer "
	secureURLScheme      = "https"
	podPhaseRunning      = "Running"
	podConditionReady    = "Ready"
	conditionTrue        = "True"
	nvidiaGPUResource    = "nvidia.com/gpu"
	amdGPUResource       = "amd.com/gpu"
	kubernetesHostEnv    = "KUBERNETES_SERVICE_HOST"
	kubernetesPortEnv    = "KUBERNETES_SERVICE_PORT_HTTPS"
	defaultHTTPSPort     = "443"
)

var _ Enricher = (*kubernetesEnricher)(nil)

// kubernetesEnricher lists Pods once per observation refresh and maps EndpointSlice
// targetRef metadata to Pod status, node name and declared GPU request.
type kubernetesEnricher struct {
	namespace string
	baseURL   string
	tokenFile string
	client    *http.Client
}

// NewKubernetes constructs the Kubernetes implementation behind Enricher.
func NewKubernetes(config Config) (Enricher, error) {
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
	if err != nil || parsed.Scheme != secureURLScheme || parsed.Host == "" {
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
	return &kubernetesEnricher{namespace: config.Namespace, baseURL: baseURL, tokenFile: config.TokenFile, client: client}, nil
}

func (p *kubernetesEnricher) Enrich(ctx context.Context, backends []backend.Backend) (map[backend.ID]Identity, error) {
	var pods podList
	if err := p.getJSON(ctx, "/api/v1/namespaces/"+url.PathEscape(p.namespace)+"/pods", &pods); err != nil {
		return nil, err
	}
	byName := make(map[string]pod, len(pods.Items))
	for _, item := range pods.Items {
		byName[item.Metadata.Name] = item
	}
	identities := make(map[backend.ID]Identity, len(backends))
	for _, candidate := range backends {
		podName := candidate.Metadata[backend.MetadataPodName]
		identity := Identity{PodName: podName, Status: StatusUnavailable}
		if podName == "" {
			identity.Error = "EndpointSlice backend has no Pod targetRef"
			identities[candidate.ID] = identity
			continue
		}
		item, ok := byName[podName]
		if !ok {
			identity.Error = "Pod targetRef is not present in namespace snapshot"
			identities[candidate.ID] = identity
			continue
		}
		identity.NodeName = item.Spec.NodeName
		identity.PodUID = item.Metadata.UID
		identity.GPURequested = gpuRequested(item.Spec.Containers)
		identity.Ready = item.Ready()
		identity.Status = StatusOK
		if !identity.Ready {
			identity.Status = StatusDegraded
			identity.Error = "Pod is not Ready"
		}
		identities[candidate.ID] = identity
	}
	return identities, nil
}

func (p *kubernetesEnricher) getJSON(ctx context.Context, requestPath string, destination any) error {
	token, err := os.ReadFile(p.tokenFile)
	if err != nil {
		return fmt.Errorf("read Kubernetes service account token: %w", err)
	}
	requestURL := strings.TrimRight(p.baseURL, "/") + path.Join("/", requestPath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set(headerAuthorization, bearerPrefix+strings.TrimSpace(string(token)))
	request.Header.Set(headerAccept, mediaTypeJSON)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxAPIErrorBodyBytes))
		return fmt.Errorf("Kubernetes Pod API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(destination); err != nil {
		return fmt.Errorf("decode Kubernetes Pod API response: %w", err)
	}
	return nil
}

func inClusterAPIURL() string {
	host := strings.TrimSpace(os.Getenv(kubernetesHostEnv))
	if host == "" {
		return ""
	}
	port := strings.TrimSpace(os.Getenv(kubernetesPortEnv))
	if port == "" {
		port = defaultHTTPSPort
	}
	return secureURLScheme + "://" + net.JoinHostPort(host, port)
}

type podList struct {
	Items []pod `json:"items"`
}

type pod struct {
	Metadata struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
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
	if p.Status.Phase != podPhaseRunning {
		return false
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == podConditionReady {
			return condition.Status == conditionTrue
		}
	}
	return false
}

func gpuRequested(containers []container) float64 {
	var total float64
	for _, container := range containers {
		value := container.Resources.Requests[nvidiaGPUResource]
		if value == "" {
			value = container.Resources.Limits[nvidiaGPUResource]
		}
		if value == "" {
			value = container.Resources.Requests[amdGPUResource]
		}
		if value == "" {
			value = container.Resources.Limits[amdGPUResource]
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
