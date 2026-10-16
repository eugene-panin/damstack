// Package plan works out what joins a project with a module: the providers of
// the capabilities it requires that the project does not have yet, theirs in
// turn, each after what it requires.
package plan

import (
	"fmt"
	"slices"
	"strings"

	"github.com/eugene-panin/damstack/internal/manifest"
)

// Module is a module as the plan knows it: from the catalog, or better, from
// its manifest.
type Module struct {
	Name string
	// Platform is the platform it goes on; empty for any.
	Platform    string
	Description string
	Provides    []manifest.Capability
	Requires    []manifest.Need
	Uses        []manifest.Need
	// Recommended puts it first where several provide a capability.
	Recommended bool
	// Settings are its answers and settings once they are known; nil before,
	// when a need under a condition may hold or not.
	Settings map[string]any
}

// FromManifest is the module of a manifest, with its settings when known.
func FromManifest(m *manifest.Manifest, platform string, settings map[string]any) Module {
	return Module{Name: m.Name, Platform: platform, Description: m.Description, Provides: manifest.Capabilities(m.Provides),
		Requires: m.Requires.Capabilities, Uses: m.Uses, Settings: settings}
}

func (m Module) provides(c manifest.Capability) bool { return slices.Contains(m.Provides, c) }

// Input is what a plan is worked out from.
type Input struct {
	// Root is the module being added.
	Root Module
	// Platform is the name of the project's platform.
	Platform string
	// Have are the capabilities the project has already, each with the names
	// of what provides it.
	Have map[manifest.Capability][]string
	// Catalog are the apps that may join the plan.
	Catalog []Module
	// Known are modules by name as known better than the catalog says, such as
	// from their manifests and answers.
	Known map[string]Module
	// Chosen are the providers picked before, by capability; Choose picks one
	// of several candidates, the recommended first, and adds to it.
	Chosen map[manifest.Capability]string
	Choose func(c manifest.Capability, by string, candidates []Module) (string, error)
}

// Plan is what joins the project, in order.
type Plan struct {
	// Modules are each after what it requires; the root is among them.
	Modules []Module
	// For says, by module, which capabilities of what other modules it joins
	// for.
	For map[string][]string
	// Maybe are needs under a condition of modules whose settings are not
	// known yet.
	Maybe []Maybe
}

// Maybe is a need of a module that may hold once its settings are known.
type Maybe struct {
	Module string
	Need   manifest.Need
}

// Names are the names of the modules of the plan, in order.
func (p Plan) Names() []string {
	out := make([]string, len(p.Modules))
	for i, m := range p.Modules {
		out[i] = m.Name
	}
	return out
}

// MissingError is a capability a module requires that nothing provides.
type MissingError struct {
	Module     string
	Capability manifest.Capability
	Platform   string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("%s requires %s, which no module of the project provides, nor any app of the catalog for %s; "+
		"add one that provides it first", e.Module, e.Capability, e.Platform)
}

// CycleError is modules that require each other.
type CycleError struct{ Path []string }

func (e *CycleError) Error() string {
	return fmt.Sprintf("these modules require each other, so none can go first: %s", strings.Join(e.Path, " → "))
}

// Resolve works out the plan: for each capability a module of it requires and
// the project does not have, a provider joins, until nothing is missing.
func Resolve(in Input) (Plan, error) {
	if in.Chosen == nil {
		in.Chosen = map[manifest.Capability]string{}
	}
	mods := map[string]Module{in.Root.Name: in.Root}
	names := []string{in.Root.Name}
	edges := map[string][]string{}
	pl := Plan{For: map[string][]string{}}
	for i := 0; i < len(names); i++ {
		name := names[i]
		sure, maybe := manifest.Split(mods[name].Requires, mods[name].Settings)
		for _, n := range maybe {
			pl.Maybe = append(pl.Maybe, Maybe{Module: name, Need: n})
		}
		for _, c := range manifest.NeedCapabilities(sure) {
			if len(in.Have[c]) > 0 {
				continue
			}
			by := ""
			for _, other := range names {
				if mods[other].provides(c) {
					by = other
					break
				}
			}
			if by == "" {
				m, err := in.pick(c, name, mods)
				if err != nil {
					return Plan{}, err
				}
				by = m.Name
				mods[by] = m
				names = append(names, by)
			}
			if by != name {
				edges[name] = append(edges[name], by)
				pl.For[by] = append(pl.For[by], c.String()+" for "+name)
			}
		}
	}
	order, err := sorted(names, edges)
	if err != nil {
		return Plan{}, err
	}
	for _, name := range order {
		pl.Modules = append(pl.Modules, mods[name])
	}
	return pl, nil
}

// pick is the provider of c that joins the plan for the module by.
func (in Input) pick(c manifest.Capability, by string, planned map[string]Module) (Module, error) {
	var candidates []Module
	for _, m := range in.Catalog {
		if _, there := planned[m.Name]; there || !m.provides(c) || (m.Platform != "" && m.Platform != in.Platform) {
			continue
		}
		if known, ok := in.Known[m.Name]; ok {
			known.Recommended = m.Recommended
			m = known
		}
		candidates = append(candidates, m)
	}
	slices.SortStableFunc(candidates, func(a, b Module) int {
		switch {
		case a.Recommended == b.Recommended:
			return 0
		case a.Recommended:
			return -1
		}
		return 1
	})
	if name, ok := in.Chosen[c]; ok {
		if i := slices.IndexFunc(candidates, func(m Module) bool { return m.Name == name }); i >= 0 {
			return candidates[i], nil
		}
	}
	switch len(candidates) {
	case 0:
		return Module{}, &MissingError{Module: by, Capability: c, Platform: in.Platform}
	case 1:
		return candidates[0], nil
	}
	if in.Choose == nil {
		return candidates[0], nil
	}
	name, err := in.Choose(c, by, candidates)
	if err != nil {
		return Module{}, err
	}
	i := slices.IndexFunc(candidates, func(m Module) bool { return m.Name == name })
	if i < 0 {
		return Module{}, fmt.Errorf("%s is not one of the modules that provide %s", name, c)
	}
	in.Chosen[c] = name
	return candidates[i], nil
}

// sorted orders names so that each comes after those its edges name, keeping
// their order otherwise.
func sorted(names []string, edges map[string][]string) ([]string, error) {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var out, path []string
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			i := slices.Index(path, name)
			return &CycleError{Path: append(slices.Clone(path[i:]), name)}
		}
		state[name] = visiting
		path = append(path, name)
		for _, dep := range edges[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[name] = done
		out = append(out, name)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RequiredBy are the modules of a project that require a capability only
// target provides there, so that target cannot go while they stay. installed
// are the project's apps, platform what its platform provides.
func RequiredBy(target string, installed []Module, platform []manifest.Capability) []string {
	var out []string
	var gives []manifest.Capability
	for _, m := range installed {
		if m.Name == target {
			gives = m.Provides
		}
	}
	for _, m := range installed {
		if m.Name == target {
			continue
		}
		sure, _ := manifest.Split(m.Requires, m.Settings)
		for _, c := range manifest.NeedCapabilities(sure) {
			if !slices.Contains(gives, c) || slices.Contains(platform, c) {
				continue
			}
			other := slices.ContainsFunc(installed, func(o Module) bool { return o.Name != target && o.Name != m.Name && o.provides(c) })
			if !other {
				out = append(out, m.Name)
				break
			}
		}
	}
	return out
}
