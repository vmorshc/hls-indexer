// Package config embeds default.yaml so the binary carries its defaults.
package config

import _ "embed"

//go:embed default.yaml
var Default []byte
