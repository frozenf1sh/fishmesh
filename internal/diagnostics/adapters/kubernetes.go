package adapters

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
	"strings"

	"github.com/frozenf1sh/fishmesh/internal/diagnostics/application"
	"github.com/frozenf1sh/fishmesh/internal/diagnostics/domain"
)

const maxKubernetesResponseBytes = 4 << 20

// KubernetesEventsTool reads only namespace-scoped core Events and Pods. It
// deliberately uses the in-cluster REST API instead of exposing kubectl or a
// general-purpose command runner to the diagnosis engine.
type KubernetesEventsTool struct {
	Namespace  string
	BaseURL    string
	TokenFile  string
	HTTPClient *http.Client
	Clock      application.Clock
}

func (t KubernetesEventsTool) Descriptor() domain.ToolDescriptor {
	return domain.ToolDescriptor{Name: "query_kubernetes_events", Description: "读取 namespace-scoped Kubernetes Warning 事件和 Pod 状态"}
}

func (t KubernetesEventsTool) Collect(ctx context.Context, _ domain.Incident) domain.Signal {
	clock := defaultClock(t.Clock)
	signal := domain.Signal{Name: t.Descriptor().Name, Source: t.Namespace, Status: domain.SignalUnavailable, ObservedAt: clock()}
	if strings.TrimSpace(t.Namespace) == "" {
		signal.Error = "Kubernetes namespace is not configured"
		return signal
	}
	baseURL := strings.TrimRight(strings.TrimSpace(t.BaseURL), "/")
	if baseURL == "" {
		baseURL = inClusterAPIURL()
	}
	if baseURL == "" {
		signal.Error = "not running in Kubernetes and no API URL is configured"
		return signal
	}
	token, err := os.ReadFile(t.TokenFile)
	if err != nil {
		signal.Error = fmt.Sprintf("read service account token: %v", err)
		return signal
	}
	client := t.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	var events coreEventList
	var pods corePodList
	var failures []string
	if err := t.getJSON(ctx, client, baseURL, token, "/api/v1/namespaces/"+url.PathEscape(t.Namespace)+"/events", &events); err != nil {
		failures = append(failures, "events: "+err.Error())
	}
	if err := t.getJSON(ctx, client, baseURL, token, "/api/v1/namespaces/"+url.PathEscape(t.Namespace)+"/pods", &pods); err != nil {
		failures = append(failures, "pods: "+err.Error())
	}
	warningEvents := 0
	warningReasons := make([]string, 0, 5)
	for _, event := range events.Items {
		if event.Type == "Warning" {
			warningEvents++
			if event.Reason != "" && len(warningReasons) < 5 {
				warningReasons = append(warningReasons, event.Reason)
			}
		}
	}
	readyPods, notReadyPods, failedPods := podStatusCounts(pods)
	if len(failures) == 2 {
		signal.Error = strings.Join(failures, "; ")
		return signal
	}
	signal.Status = domain.SignalOK
	if len(failures) > 0 {
		signal.Status = domain.SignalDegraded
		signal.Error = strings.Join(failures, "; ")
	}
	signal.Values = map[string]float64{"warning_events": float64(warningEvents), "pods_ready": float64(readyPods), "pods_not_ready": float64(notReadyPods), "pods_failed": float64(failedPods)}
	if len(warningReasons) > 0 {
		signal.Attributes = map[string]string{"warning_reasons": strings.Join(warningReasons, ",")}
	}
	signal.Summary = fmt.Sprintf("Kubernetes events and Pod status collected for namespace %s", t.Namespace)
	return signal
}

func (t KubernetesEventsTool) getJSON(ctx context.Context, client *http.Client, baseURL string, token []byte, requestPath string, destination any) error {
	requestURL := strings.TrimRight(baseURL, "/") + path.Join("/", requestPath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxKubernetesResponseBytes))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode API response: %w", err)
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

type coreEventList struct {
	Items []struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"items"`
}

type corePodList struct {
	Items []struct {
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func podStatusCounts(pods corePodList) (ready, notReady, failed int) {
	for _, pod := range pods.Items {
		if pod.Status.Phase == "Failed" {
			failed++
			continue
		}
		isReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				isReady = true
				break
			}
		}
		if isReady {
			ready++
		} else {
			notReady++
		}
	}
	return ready, notReady, failed
}
