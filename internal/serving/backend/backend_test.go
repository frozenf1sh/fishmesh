package backend

import "testing"

func TestBackendValidate(t *testing.T) {
	for name, test := range map[string]struct {
		backend Backend
		valid   bool
	}{
		"valid HTTP":   {Backend{ID: "a", URL: "http://backend:8000"}, true},
		"valid HTTPS":  {Backend{ID: "a", URL: "https://backend:8443/v1"}, true},
		"missing ID":   {Backend{URL: "http://backend:8000"}, false},
		"relative URL": {Backend{ID: "a", URL: "/v1"}, false},
		"wrong scheme": {Backend{ID: "a", URL: "unix:///socket"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := test.backend.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

func TestNewHTTPBuildsStableEndpointIdentity(t *testing.T) {
	metadata := map[string]string{MetadataPodName: "model-a"}
	first := NewHTTP("10.0.0.2", 8000, metadata)
	second := NewHTTP("10.0.0.2", 8000, metadata)
	metadata[MetadataPodName] = "changed"
	if first.ID != second.ID || first.URL != "http://10.0.0.2:8000" {
		t.Fatalf("endpoint identity is not stable: first=%+v second=%+v", first, second)
	}
	if first.Metadata[MetadataPodName] != "model-a" {
		t.Fatalf("backend metadata aliases caller map: %+v", first.Metadata)
	}
}
