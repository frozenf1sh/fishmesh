package adapters

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	dto "github.com/prometheus/client_model/go"
)

const maxMetricsBodyBytes = 8 << 20

func fetchMetricFamilies(ctx context.Context, client *http.Client, targetURL string) (map[string]*dto.MetricFamily, error) {
	if strings.TrimSpace(targetURL) == "" {
		return nil, fmt.Errorf("metrics URL must not be empty")
	}
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/plain; version=0.0.4")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint returned %s", response.Status)
	}
	return parseMetricFamilies(io.LimitReader(response.Body, maxMetricsBodyBytes))
}
