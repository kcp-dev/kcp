/*
Copyright 2025 The Kubernetes Authors.
Modifications Copyright 2025 The KCP Authors.

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

package imports

import (
	"go/token"
	"path/filepath"
	"strings"

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"
	"k8s.io/klog/v2"
)

// NewImportTrackerForPackage returns a tracker that will automatically prefix any
// import from github.com/kcp-dev/* with "kcp", so such imports are easily distinguishable
// from the generated single-cluster code.
func NewImportTrackerForPackage(local string, typesToAdd ...*types.Type) *namer.DefaultImportTracker {
	tracker := generator.NewImportTrackerForPackage(local, typesToAdd...)
	tracker.LocalName = func(name types.Name) string { return goTrackerLocalName(tracker, local, name) }

	return tracker
}

// goTrackerLocalName is copied from upstream and just extended to include the kcp prefix.
func goTrackerLocalName(tracker namer.ImportTracker, localPkg string, t types.Name) string {
	path := t.Package
	isKcp := strings.HasPrefix(path, "github.com/kcp-dev/")

	if path == "github.com/kcp-dev/logicalcluster/v3" {
		return "logicalcluster"
	}

	// Using backslashes in package names causes gengo to produce Go code which
	// will not compile with the gc compiler. See the comment on GoSeperator.
	if strings.ContainsRune(path, '\\') {
		klog.Warningf("Warning: backslash used in import path '%v', this is unsupported.\n", path)
	}
	localLeaf := filepath.Base(localPkg)

	dirs := strings.Split(path, namer.GoSeperator)
	for n := len(dirs) - 1; n >= 0; n-- {
		// follow kube convention of not having anything between directory names
		name := strings.Join(dirs[n:], "")
		name = strings.ReplaceAll(name, "_", "")
		// These characters commonly appear in import paths for go
		// packages, but aren't legal go names. So we'll sanitize.
		name = strings.ReplaceAll(name, ".", "")
		name = strings.ReplaceAll(name, "-", "")

		if isKcp {
			name = "kcp" + name
		}

		if _, found := tracker.PathOf(name); found || name == localLeaf {
			// This name collides with some other package.
			// Or, this name is the same name as the local package,
			// which we avoid because it can be confusing. For example,
			// if the local package is v1, we to avoid importing
			// another package using the v1 name, and instead import
			// it with a more qualified name, such as metav1.
			continue
		}

		// If the import name is a Go keyword, prefix with an underscore.
		if token.Lookup(name).IsKeyword() {
			name = "_" + name
		}
		return name
	}
	panic("can't find import for " + path)
}
