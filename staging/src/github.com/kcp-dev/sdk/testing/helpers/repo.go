/*
Copyright 2025 The kcp Authors.

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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
)

var (
	readFile   = os.ReadFile
	statIsDir  = statIsDirImpl
	sourceFile = sourceFileImpl
	frameFiles = frameFilesImpl
	osStat     = os.Stat
)

// RepositoryDir attempts to find the kcp-dev/kcp repository on disk
// by looking for its go.mod file. If the KCP_REPO_DIR environment variable is set,
// its value will be returned directly as an absolute path.
func RepositoryDir() (string, error) {
	const envVar = "KCP_REPO_DIR"

	if repo := os.Getenv(envVar); repo != "" {
		absRepo, isDir, err := statIsDir(repo)
		if err != nil {
			return "", fmt.Errorf("could not stat %s (%s): %w", envVar, repo, err)
		}
		if !isDir {
			return "", fmt.Errorf("%s is not a directory: %s", envVar, repo)
		}
		return absRepo, nil
	}

	// Start with this source file's path. This works for the monorepo case -
	// i.e. when this code is called from:
	//   kcp-dev/kcp/staging/src/github.com/kcp-dev/sdk/testing/helpers/repo.go
	var candidates []string
	if f := sourceFile(); f != "" {
		candidates = append(candidates, f)
	}

	// Also collect caller source files from the call stack that are not
	// inside the go caches. This covers the case where the sdk repository
	// is consumed as an external module dependency.
	for _, f := range frameFiles() {
		if !strings.Contains(f, filepath.Join("pkg", "mod")) &&
			!strings.Contains(f, filepath.Join(".cache", "go-build")) {
			candidates = append(candidates, f)
		}
	}

	// Go through each candidate and try to find the kcp go.mod file in its tree.
	for _, sourceFile := range candidates {
		currentDir := filepath.Dir(sourceFile)
		for {
			modPath := filepath.Join(currentDir, "go.mod")
			data, err := readFile(modPath)
			if err == nil {
				if strings.HasPrefix(strings.TrimSpace(string(data)), "module github.com/kcp-dev/kcp") {
					return currentDir, nil
				}
			}
			preDir := currentDir
			currentDir, err = filepath.Abs(filepath.Join(currentDir, ".."))
			if err != nil {
				break
			}
			if preDir == currentDir {
				break
			}
		}
	}

	return "", fmt.Errorf("could not find the kcp repository root; "+
		"try setting the %s environment variable", envVar)
}

// RepositoryBinDir returns the absolute path of <repo-dir>/bin. That's where `make build` produces our binaries.
func RepositoryBinDir() (string, error) {
	repoDir, err := RepositoryDir()
	return filepath.Join(repoDir, "bin"), err
}

// statIsDirImpl checks if the given path is a directory.
func statIsDirImpl(path string) (string, bool, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	stat, err := osStat(absPath)
	if err != nil {
		return "", false, err
	}
	return absPath, stat.IsDir(), nil
}

func sourceFileImplAnchor() {}

// sourceFileImpl uses runtime.FuncForPC on a function in this file
// to locate this source file location on disk.
func sourceFileImpl() string {
	fn := goruntime.FuncForPC(reflect.ValueOf(sourceFileImplAnchor).Pointer())
	if fn == nil {
		return ""
	}
	file, _ := fn.FileLine(fn.Entry())
	return file
}

// frameFilesImpl returns a list of source files from the call stack frames.
func frameFilesImpl() []string {
	pcs := make([]uintptr, 64)
	n := goruntime.Callers(0, pcs)
	frames := goruntime.CallersFrames(pcs[:n])
	var files []string
	for {
		frame, more := frames.Next()
		if frame.File != "" {
			files = append(files, frame.File)
		}
		if !more {
			break
		}
	}
	return files
}
