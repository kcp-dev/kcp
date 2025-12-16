/*
Copyright 2021 The KCP Authors.

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

package framework

import (
	"path/filepath"

	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
)

// DefaultTokenAuthFile returns the default auth tokens file.
var DefaultTokenAuthFile string

func init() {
	repo, err := kcptestinghelpers.RepositoryDir()
	if err != nil {
		panic(err)
	}

	DefaultTokenAuthFile = filepath.Join(repo, "test", "e2e", "framework", "auth-tokens.csv")

	var args []string
	args = append(args, "--token-auth-file", DefaultTokenAuthFile) //nolint:gocritic // no.
	args = append(args, "--feature-gates=WorkspaceMounts=true,CacheAPIs=true,WorkspaceAuthentication=true,KcpNativeGarbageCollector=false")

	kcptesting.InitSharedKcpServer(kcptestingserver.WithCustomArguments(args...))
}
