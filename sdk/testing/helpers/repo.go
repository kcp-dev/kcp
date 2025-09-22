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

package helpers

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
)

// RepositoryDir returns the absolute path of kcp-dev/kcp repository on disk.
func RepositoryDir() (string, error) {
	// Caller(0) returns the path to the calling test file rather than the path to this framework file. That
	// precludes assuming how many directories are between the file and the repo root. It's therefore necessary
	// to search in the hierarchy for an indication of a path that looks like the repo root.
	_, sourceFile, _, _ := goruntime.Caller(0)
	currentDir := filepath.Dir(sourceFile)
	for {
		// go.mod should always exist in the repo root
		if _, err := os.Stat(filepath.Join(currentDir, ".git")); err == nil {
			break
		} else if errors.Is(err, os.ErrNotExist) {
			preDir := currentDir
			currentDir, err = filepath.Abs(filepath.Join(currentDir, ".."))
			if err != nil {
				return "", err
			}
			if preDir == currentDir {
				return "", errors.New("could not find kcp repository root")
			}
		} else {
			return "", err
		}
	}
	return currentDir, nil
}

// RepositoryBinDir returns the absolute path of <repo-dir>/bin. That's where `make build` produces our binaries.
func RepositoryBinDir() (string, error) {
	repoDir, err := RepositoryDir()
	return filepath.Join(repoDir, "bin"), err
}
