// Package release pins what a build of damstack works with.
package release

var Version = "dev"

// Toolbox is the release of damstack-toolbox the steps run with, and the
// SHA-256 of its archive for each kind of Mac.
const Toolbox = "1.1.0"

var ToolboxSHA256 = map[string]string{
	"arm64": "658d3eadeda146ad964471ed737224fcc93c4cbaef40e35fa3ac410dd944b6d5",
	"amd64": "8db4341aba1bcb4d90021c297e565f995d944cec3d2f58d96492876909431fe5",
}
