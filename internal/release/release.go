// Package release pins what a build of damstack works with.
package release

var Version = "dev"

const (
	Image    = "ghcr.io/eugene-panin/hashistack-starter"
	ImageTag = "v0.3.0"
)

func ImageRef() string { return Image + ":" + ImageTag }
