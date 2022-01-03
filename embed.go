/*
Copyright 2022 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kcp

import "embed"

// This file should ONLY be used to define variables with the content of data files
// accessible from the root of the source code, that should be embedded inside
// GO code through go:embed annotations
// It is required to define this file at the root of the module since
// go:embed cannot reference files in parent directories relatively to the current package.

//go:embed config
var ConfigDir embed.FS
