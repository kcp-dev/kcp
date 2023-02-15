/*
Copyright 2023 The KCP Authors.

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

package plugin

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

type koConfig struct {
	Package         string
	KindClusterName string
}

func koBuild(t *testing.T, cfg koConfig) string {
	commandLine := []string{
		"ko",
		"build",
		fmt.Sprintf("--platform=linux/%s", runtime.GOARCH),
		cfg.Package,
	}

	out, err := runCmd(commandLine, withEnvVariable("KO_DOCKER_REPO", "kind.local"), withEnvVariable("KIND_CLUSTER_NAME", cfg.KindClusterName))
	if err != nil {
		t.Fatalf("failed to run ko %v", err)
	}
	return strings.TrimSuffix(out.String(), "\n")
}
