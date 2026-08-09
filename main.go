package main

import "github.com/example/glusterfs-plugin/internal/plugin"

var version = "dev"
var revision = "unknown"

func main() {
	plugin.Main(version, revision)
}
