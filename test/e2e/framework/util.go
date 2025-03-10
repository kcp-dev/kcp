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
	"embed"
	"math/rand"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/martinlindhe/base36"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

//go:embed *.csv
//go:embed *.yaml
var fs embed.FS

// WriteTokenAuthFile writes the embedded token file to the current
// test's data dir.
//
// Persistent servers can target the file in the source tree with
// `--token-auth-file` and test-managed servers can target a file
// written to a temp path. This avoids requiring a test to know the
// location of the token file.
//
// TODO(marun) Is there a way to avoid embedding by determining the
// path to the file during test execution?
func WriteTokenAuthFile(t *testing.T) string {
	t.Helper()
	return WriteEmbedFile(t, "auth-tokens.csv")
}

func WriteEmbedFile(t *testing.T, source string) string {
	t.Helper()
	data, err := fs.ReadFile(source)
	require.NoErrorf(t, err, "error reading embed file: %q", source)

	targetPath := path.Join(t.TempDir(), source)
	targetFile, err := os.Create(targetPath)
	require.NoErrorf(t, err, "failed to create target file: %q", targetPath)
	defer targetFile.Close()

	_, err = targetFile.Write(data)
	require.NoError(t, err, "error writing target file: %q", targetPath)

	return targetPath
}

type ArtifactFunc func(*testing.T, func() (runtime.Object, error))

// StaticTokenUserConfig returns a user config based on static user tokens defined in "test/e2e/framework/auth-tokens.csv".
// The token being used is "[username]-token".
func StaticTokenUserConfig(username string, cfg *rest.Config) *rest.Config {
	return ConfigWithToken(username+"-token", cfg)
}

func ConfigWithToken(token string, cfg *rest.Config) *rest.Config {
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.CertData = nil
	cfgCopy.KeyData = nil
	cfgCopy.BearerToken = token
	return cfgCopy
}

// UniqueGroup returns a unique API group with the given suffix by prefixing
// some random 8 character base36 string. suffix must start with a dot if the
// random string should be dot-separated.
func UniqueGroup(suffix string) string {
	ret := strings.ToLower(base36.Encode(rand.Uint64())[:8]) + suffix
	if ret[0] >= '0' && ret[0] <= '9' {
		return "a" + ret[1:]
	}
	return ret
}
