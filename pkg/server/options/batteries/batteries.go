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

package batteries

import (
	"k8s.io/apimachinery/pkg/util/sets"

	_ "github.com/kcp-dev/kcp/pkg/features"
)

const (
	// WorkspaceTypes leads to creation of a number of default types beyond the universal type.
	WorkspaceTypes = "workspace-types"

	// Admin leads to creating a local admin.kubeconfig and a token hash file to access kcp as shard admin.
	Admin = "admin"

	// User leads to an additional user named "user" in the admin.kubeconfig that is not admin. Requires the "admin" battery to be enabled.
	User = "user"

	// MetricsViewer leads to an additional service account named "metrics" in the root namespace that can view metrics.
	MetricsViewer = "metrics-viewer"
)

var All = sets.New[string](
	WorkspaceTypes,
	Admin,
	User,
	MetricsViewer,
)

var Defaults = sets.New[string](
	WorkspaceTypes,
	Admin,
)
