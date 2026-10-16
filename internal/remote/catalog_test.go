package remote

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hashicorp/consul/api"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/plan"
)

type fakeCatalog struct {
	services map[string][]string
	nodes    map[string][]*api.CatalogService
	down     bool
	tags     []string
}

func (f *fakeCatalog) Services(*api.QueryOptions) (map[string][]string, *api.QueryMeta, error) {
	if f.down {
		return nil, nil, errors.New("dial tcp 10.77.0.1:8501: i/o timeout")
	}
	return f.services, nil, nil
}

func (f *fakeCatalog) Service(service, tag string, _ *api.QueryOptions) ([]*api.CatalogService, *api.QueryMeta, error) {
	f.tags = append(f.tags, service+" "+tag)
	return f.nodes[service], nil, nil
}

func TestProvidersAreTheServicesTaggedWithTheirCapability(t *testing.T) {
	c := &fakeCatalog{
		services: map[string][]string{
			"mimir":      {"damstack.provides=metrics@1", "http"},
			"prometheus": {"damstack.provides=metrics"},
			"loki":       {"damstack.provides=logs@1"},
			"vault":      {"active"},
		},
		nodes: map[string][]*api.CatalogService{
			"mimir":      {{Address: "10.77.0.1", ServicePort: 9009, ServiceMeta: map[string]string{"path": "/prometheus"}}},
			"prometheus": {{Address: "10.77.0.1", ServiceAddress: "10.77.0.9", ServicePort: 9090}},
		},
	}
	f := &Finder{Catalog: c}
	metrics, _ := manifest.ParseCapability("metrics@1")
	got, err := f.Find(t.Context(), metrics)
	want := []plan.Provider{
		{Name: "mimir", Address: "10.77.0.1", Port: 9009, Meta: map[string]string{"path": "/prometheus"}},
		{Name: "prometheus", Address: "10.77.0.9", Port: 9090, Meta: map[string]string{}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v", got, err)
	}
	if !reflect.DeepEqual(c.tags, []string{"mimir damstack.provides=metrics@1", "prometheus damstack.provides=metrics"}) {
		t.Errorf("asked %v", c.tags)
	}

	s3, _ := manifest.ParseCapability("s3@1")
	if got, err := f.Find(t.Context(), s3); err != nil || len(got) != 0 {
		t.Errorf("none: %+v, %v", got, err)
	}
	c.down = true
	if _, err := f.Find(t.Context(), metrics); !errors.Is(err, ErrUnreachable) {
		t.Errorf("down: %v", err)
	}
}
