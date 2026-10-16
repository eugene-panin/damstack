package plan

import (
	"context"
	"slices"

	"github.com/eugene-panin/damstack/internal/manifest"
)

// Tag is the tag of a service in the platform's Consul that provides a
// capability: damstack.provides=metrics@1.
const Tag = "damstack.provides"

// TagOf is the tag of a service that provides c.
func TagOf(c manifest.Capability) string { return Tag + "=" + c.String() }

// Provider is a service that provides a capability, and how to reach it.
type Provider struct {
	Name    string            `json:"name"`
	Address string            `json:"address"`
	Port    int               `json:"port"`
	Meta    map[string]string `json:"meta"`
}

// Providers are the providers a module is given, by capability, such as
// {"metrics@1": [{"name": "mimir", ...}]}.
type Providers map[string][]Provider

// Finder finds the services that provide a capability on the platform, such
// as in its Consul.
type Finder interface {
	Find(ctx context.Context, c manifest.Capability) ([]Provider, error)
}

// Discover finds the providers of caps with f, and reports whether it could
// look on the platform. When f is nil or does not answer, the providers are
// those of fallback, the modules of the project by capability, by name only.
func Discover(ctx context.Context, f Finder, caps []manifest.Capability, fallback map[manifest.Capability][]string) (Providers, bool) {
	if f != nil {
		out := Providers{}
		ok := true
		for _, c := range caps {
			found, err := f.Find(ctx, c)
			if err != nil {
				ok = false
				break
			}
			out[c.String()] = found
		}
		if ok {
			return out, true
		}
	}
	out := Providers{}
	for _, c := range caps {
		for _, name := range fallback[c] {
			out[c.String()] = append(out[c.String()], Provider{Name: name, Meta: map[string]string{}})
		}
	}
	return out, false
}

// Only is p with only the providers named, by capability.
func (p Providers) Only(names map[string][]string) Providers {
	out := Providers{}
	for c, list := range p {
		for _, prov := range list {
			if slices.Contains(names[c], prov.Name) {
				out[c] = append(out[c], prov)
			}
		}
	}
	return out
}
