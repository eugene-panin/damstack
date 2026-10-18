package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
)

func TestADomainGoesOnThePlatformAndProvidesLikeAnApp(t *testing.T) {
	p, pm, pdir, password := planProject(t)
	files := testApp("cloudflare", "provides: [public-dns@1]\nrequires: {provides: [demo]}\n")
	files[manifest.File] = strings.Replace(files[manifest.File], "kind: app", "kind: domain", 1)
	dm, ddir := writeStack(t, files)
	if err := fits(pm, dm); err != nil || !dm.IsDomain() {
		t.Fatalf("a domain does not go on the platform: %v", err)
	}
	p.Meta.Apps = append(p.Meta.Apps, project.StackRef{Name: "cloudflare", URL: "file://" + ddir, Tag: ops.DevTag})
	am, adir := writeStack(t, testApp("site", "requires:\n  provides: [demo]\n  capabilities: [public-dns@1]\n"))
	var out bytes.Buffer
	s := newCLI(t.Context(), strings.NewReader(""), &out, &out)
	order, err := addPlan(t.Context(), p, pm, pdir, &planned{m: am, dir: adir}, terminalFlow(s, p, password, "site", nil, true))
	if err != nil || len(order) != 1 || order[0].m.Name != "site" {
		t.Errorf("got %v, %v\n%s", order, err, out.String())
	}
}
