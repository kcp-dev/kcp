/*
Copyright 2025 The KCP Authors.

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

package testing

import (
	"embed"
	"os"
	"path"

	"github.com/stretchr/testify/require"
)

// copyEmbeddedToTempDir copies the given file to a temp dir and returns the path.
func copyEmbeddedToTempDir(t TestingT, fs embed.FS, source string) string {
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
