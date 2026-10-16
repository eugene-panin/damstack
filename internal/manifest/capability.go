package manifest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Capability is what a module provides or needs, as name@major: a word and
// the major version of what it promises. A bare name reads as name@1.
type Capability struct {
	Name  string
	Major int
}

var capabilityRe = regexp.MustCompile(`^([a-z][a-z0-9-]{1,40})(?:@([0-9]{1,6}))?$`)

// ParseCapability reads name@major, or a bare name as name@1.
func ParseCapability(s string) (Capability, error) {
	m := capabilityRe.FindStringSubmatch(s)
	if m == nil {
		return Capability{}, fmt.Errorf("%q is not a capability: name@major, the name lowercase letters, digits and hyphens, such as metrics@1", s)
	}
	c := Capability{Name: m[1], Major: 1}
	if m[2] != "" {
		c.Major, _ = strconv.Atoi(m[2])
	}
	return c, nil
}

func (c Capability) String() string { return c.Name + "@" + strconv.Itoa(c.Major) }

// Capabilities are the capabilities of list, those that read as one.
func Capabilities(list []string) []Capability {
	var out []Capability
	for _, s := range list {
		if c, err := ParseCapability(s); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// Has reports whether provides, a list of capabilities, has need.
func Has(provides []string, need string) bool {
	want, err := ParseCapability(need)
	if err != nil {
		return false
	}
	for _, c := range Capabilities(provides) {
		if c == want {
			return true
		}
	}
	return false
}

// Need is a capability a module requires or uses, under When when it is set:
// the same condition as a question's, over the module's own settings.
type Need struct {
	Capability string
	When       string
}

func (n *Need) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		n.Capability = node.Value
		return nil
	case yaml.MappingNode:
		var v struct {
			Capability string `yaml:"capability"`
			When       string `yaml:"when"`
		}
		if err := node.Decode(&v); err == nil && v.Capability != "" {
			for i := 0; i < len(node.Content); i += 2 {
				if key := node.Content[i].Value; key != "capability" && key != "when" {
					return fmt.Errorf("line %d: unknown field %s: a capability is name@major, or {capability: name@major, when: <condition>}", node.Content[i].Line, key)
				}
			}
			n.Capability, n.When = v.Capability, v.When
			return nil
		}
	}
	return fmt.Errorf("line %d: a capability is name@major, or {capability: name@major, when: <condition>}", node.Line)
}

func (n Need) MarshalYAML() (any, error) {
	if n.When == "" {
		return n.Capability, nil
	}
	return map[string]string{"capability": n.Capability, "when": n.When}, nil
}

// Holds reports whether the need holds for settings: always without When, and
// with it as Holds says over Flatten(settings).
func (n Need) Holds(settings map[string]any) bool {
	return Holds(n.When, Flatten(settings))
}

// Flatten is settings with each nested setting under its dotted path as well,
// such as postgres.mode, for a condition to reach into them.
func Flatten(settings map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			out[prefix+k] = v
			if sub, ok := v.(map[string]any); ok {
				walk(prefix+k+".", sub)
			}
		}
	}
	walk("", settings)
	return out
}

// Split sorts needs by settings: those that hold, and, when settings are not
// known yet (nil), those under a condition that may.
func Split(needs []Need, settings map[string]any) (sure, maybe []Need) {
	for _, n := range needs {
		switch {
		case n.When == "":
			sure = append(sure, n)
		case settings == nil:
			maybe = append(maybe, n)
		case n.Holds(settings):
			sure = append(sure, n)
		}
	}
	return sure, maybe
}

// NeedCapabilities are the capabilities of needs, those that read as one.
func NeedCapabilities(needs []Need) []Capability {
	list := make([]string, len(needs))
	for i, n := range needs {
		list[i] = n.Capability
	}
	return Capabilities(list)
}

// checkCapabilities checks a list of capabilities at path.
func (c *checker) checkCapabilities(path string, list []string) {
	seen := map[Capability]bool{}
	for i, s := range list {
		p := fmt.Sprintf("%s[%d]", path, i)
		cp, err := ParseCapability(s)
		switch {
		case err != nil:
			c.add(p, "%v", err)
		case seen[cp]:
			c.add(p, "%s is there already", cp)
		}
		seen[cp] = true
	}
}

var settingRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_-]*)+$`)

// checkNeeds checks requires.capabilities or uses: each a capability, under a
// condition on a question, or on a dotted setting of the module's block.
func (c *checker) checkNeeds(path string, needs []Need, questions []Question) {
	list := make([]string, len(needs))
	for i, n := range needs {
		list[i] = n.Capability
	}
	c.checkCapabilities(path, list)
	for i, n := range needs {
		if n.When == "" {
			continue
		}
		p := fmt.Sprintf("%s[%d].when", path, i)
		name, _, _ := strings.Cut(n.When, "=")
		if strings.Contains(name, ".") {
			if !settingRe.MatchString(name) {
				c.add(p, "%q is not a setting: a dotted path of the app's settings, such as postgres.mode", name)
			}
			continue
		}
		c.checkWhen(p, n.When, questions)
	}
}
