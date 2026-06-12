/*
Copyright 2026 The kcp Authors.

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

package filters

import (
	"net/http"
)

const migrationDumpHandlerPath = "/apis/migration.kcp.io/v1alpha1/logicalclusterdumps"

// WithMigrationDumpHandler intercepts POSTs to the LogicalClusterDump path
// before the kube REST chain rejects the unknown migration.kcp.io API group
// with a 503. All other requests fall through to next.
func WithMigrationDumpHandler(next http.Handler, dump http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && req.URL.Path == migrationDumpHandlerPath {
			dump.ServeHTTP(w, req)
			return
		}
		next.ServeHTTP(w, req)
	}
}
