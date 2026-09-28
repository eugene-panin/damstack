package manifest

import (
	"fmt"
	"net/netip"
	"text/template"
)

// TemplateFuncs are the functions the env values of a step, and the config
// template, may call besides the builtins of text/template.
var TemplateFuncs = template.FuncMap{
	"firstHost": func(cidr string) (string, error) {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return "", fmt.Errorf("firstHost %q: %w", cidr, err)
		}
		return prefix.Masked().Addr().Next().String(), nil
	},
	"secret": func(string) string { return "" },
	"join": func(sep string, items []any) string {
		out := ""
		for i, item := range items {
			if i > 0 {
				out += sep
			}
			out += fmt.Sprint(item)
		}
		return out
	},
}
