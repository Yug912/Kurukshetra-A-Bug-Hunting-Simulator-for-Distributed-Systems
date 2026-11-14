// Package deps pins shared third-party dependencies so go mod tidy keeps them
// in go.mod before the feature branches start using them.
package deps

import (
	_ "github.com/anishathalye/porcupine"
	_ "github.com/docker/docker/client"
	_ "github.com/hashicorp/raft"
	_ "github.com/hashicorp/raft-boltdb/v2"
	_ "github.com/spf13/cobra"
	_ "gopkg.in/yaml.v3"
)
