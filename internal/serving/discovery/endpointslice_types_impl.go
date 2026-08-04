package discovery

import (
	"fmt"
	"sort"
	"strings"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	addressTypeIPv4    = "IPv4"
	addressTypeIPv6    = "IPv6"
	targetKindPod      = "Pod"
	preferredPortName  = "http"
	fallbackItemPrefix = "item-"
)

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

type addressPort struct {
	address string
	port    int32
	podName string
}

func buildBackends(items []endpointSliceResource) []backend.Backend {
	itemsByName := make(map[string]endpointSliceResource, len(items))
	for index, item := range items {
		key := item.Metadata.Name
		if key == "" {
			key = fmt.Sprintf("%s%d", fallbackItemPrefix, index)
		}
		itemsByName[key] = item
	}
	return buildBackendsFromMap(itemsByName)
}

func buildBackendsFromMap(items map[string]endpointSliceResource) []backend.Backend {
	unique := make(map[addressPort]struct{})
	for _, item := range items {
		if item.AddressType != addressTypeIPv4 && item.AddressType != addressTypeIPv6 {
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
				if strings.TrimSpace(address) == "" {
					continue
				}
				podName := ""
				if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == targetKindPod {
					podName = endpoint.TargetRef.Name
				}
				unique[addressPort{address: address, port: port, podName: podName}] = struct{}{}
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
	backends := make([]backend.Backend, 0, len(keys))
	for _, key := range keys {
		metadata := map[string]string{}
		if key.podName != "" {
			metadata[backend.MetadataPodName] = key.podName
		}
		backends = append(backends, backend.NewHTTP(key.address, int(key.port), metadata))
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
		if port.Name != nil && *port.Name == preferredPortName {
			return *port.Port, true
		}
	}
	return first, first > 0
}
