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

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Persistent mapping of test name to base temp dir used to ensure
// artifact paths have a common root across servers for a given test.
var (
	baseTempDirs     = map[string]string{}
	baseTempDirsLock = sync.Mutex{}
)

// createTempDirForTest creates the named directory with a unique base
// path derived from the name of the current test.
func createTempDirForTest(t TestingT, dirName string) (string, error) {
	t.Helper()
	baseTempDir, err := ensureBaseTempDir(t)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(baseTempDir, dirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("could not create subdir: %w", err)
	}
	return dir, nil
}

// ScratchDirs determines where artifacts and data should live for a test server.
// The passed subDir is appended to the artifact directory and should be unique
// to the test.
func ScratchDirs(t TestingT) (string, string, error) {
	t.Helper()

	artifactDir, err := createTempDirForTest(t, toTestDir(t.Name()))
	if err != nil {
		return "", "", err
	}
	return artifactDir, t.TempDir(), nil
}

// ensureBaseTempDir returns the name of a base temp dir for the
// current test, creating it if needed.
func ensureBaseTempDir(t TestingT) (string, error) {
	t.Helper()

	baseTempDirsLock.Lock()
	defer baseTempDirsLock.Unlock()
	name := t.Name()
	if _, ok := baseTempDirs[name]; !ok {
		var baseDir string
		if dir, set := os.LookupEnv("ARTIFACT_DIR"); set {
			baseDir = dir
		} else {
			baseDir = t.TempDir()
		}
		baseDir = filepath.Join(baseDir, strings.NewReplacer("\\", "_", ":", "_").Replace(t.Name()))
		if err := os.MkdirAll(baseDir, 0755); err != nil {
			return "", fmt.Errorf("could not create base dir: %w", err)
		}
		baseTempDir, err := os.MkdirTemp(baseDir, "")
		if err != nil {
			return "", fmt.Errorf("could not create base temp dir: %w", err)
		}
		baseTempDirs[name] = baseTempDir
		t.Logf("Saving test artifacts for test %q under %q.", name, baseTempDir)

		// Remove the path from the cache after test completion to
		// ensure subsequent invocations of the test (e.g. due to
		// -count=<val> for val > 1) don't reuse the same path.
		t.Cleanup(func() {
			baseTempDirsLock.Lock()
			defer baseTempDirsLock.Unlock()
			delete(baseTempDirs, name)
		})
	}
	return baseTempDirs[name], nil
}

// toTestDir converts a test name into a Unix-compatible directory name.
func toTestDir(testName string) string {
	// Insert a dash before uppercase letters in the middle of the string
	reg := regexp.MustCompile(`([a-z0-9])([A-Z])`)
	safeName := reg.ReplaceAllString(testName, "${1}-${2}")

	// Replace any remaining non-alphanumeric characters (except dashes and underscores) with underscores
	reg = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	safeName = reg.ReplaceAllString(safeName, "_")

	// Remove any leading or trailing underscores or dashes
	safeName = strings.Trim(safeName, "_-")

	// Convert to lowercase (optional, depending on your preference)
	safeName = strings.ToLower(safeName)

	return safeName
}
