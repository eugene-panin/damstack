// Package release pins what a build of damstack works with.
package release

var Version = "dev"

const (
	Image    = "ghcr.io/eugene-panin/damstack-toolbox"
	ImageTag = "1.0.0"
)

func ImageRef() string { return Image + ":" + ImageTag }
