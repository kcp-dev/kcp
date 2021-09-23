package kcp

import "embed"

// This file should ONLY be used to define variables with the content of data files
// accessible from the root of the source code, that should be embedded inside
// GO code through go:embed annotations
// It is required to define this file at the root of the module since
// go:embed cannot reference files in parent directories relatively to the current package.

//go:embed config
var ConfigDir embed.FS
