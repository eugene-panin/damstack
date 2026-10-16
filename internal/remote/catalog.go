package remote

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/plan"
)

// Catalog is the part of Consul's catalog providers are found in; *api.Catalog
// is one.
type Catalog interface {
	Services(q *api.QueryOptions) (map[string][]string, *api.QueryMeta, error)
	Service(service, tag string, q *api.QueryOptions) ([]*api.CatalogService, *api.QueryMeta, error)
}

// FindTimeout bounds a look for providers, so that a platform that does not
// answer only delays adding an app a little.
const FindTimeout = 5 * time.Second

// Finder finds the providers of a capability in the platform's Consul: the
// services tagged damstack.provides=<capability>, or for a capability of major
// 1 its bare name too.
type Finder struct{ Catalog Catalog }

// OpenFinder is a Finder over the Consul a platform names in its app_env.
func OpenFinder(env map[string]string) (*Finder, error) {
	client, err := open(env, FindTimeout)
	if err != nil {
		return nil, err
	}
	return &Finder{Catalog: client.Catalog()}, nil
}

func (f *Finder) Find(ctx context.Context, c manifest.Capability) ([]plan.Provider, error) {
	ctx, cancel := context.WithTimeout(ctx, FindTimeout)
	defer cancel()
	q := (&api.QueryOptions{}).WithContext(ctx)
	services, _, err := f.Catalog.Services(q)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	tags := []string{plan.TagOf(c)}
	if c.Major == 1 {
		tags = append(tags, plan.Tag+"="+c.Name)
	}
	var out []plan.Provider
	for _, name := range slices.Sorted(maps.Keys(services)) {
		i := slices.IndexFunc(tags, func(t string) bool { return slices.Contains(services[name], t) })
		if i < 0 {
			continue
		}
		instances, _, err := f.Catalog.Service(name, tags[i], q)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
		if len(instances) == 0 {
			continue
		}
		s := instances[0]
		address := s.ServiceAddress
		if address == "" {
			address = s.Address
		}
		meta := map[string]string{}
		maps.Copy(meta, s.ServiceMeta)
		out = append(out, plan.Provider{Name: name, Address: address, Port: s.ServicePort, Meta: meta})
	}
	return out, nil
}
