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

// ============================================================================
// DEPRECATED
//
// The impersonation header helpers have moved to the virtual workspace
// framework module, so that virtual workspaces developed outside of kcp can use
// them without depending on kcp itself:
//
//	virtual-workspace-framework/pkg/impersonation
//
// ============================================================================

package authentication

import (
	"net/http"

	"github.com/kcp-dev/virtual-workspace-framework/pkg/impersonation"
)

// HasImpersonationHeaders reports whether the request asks to impersonate
// someone, through any of the headers the apiserver's impersonation filter acts
// on.
//
// Deprecated: use impersonation.HasHeaders from
// github.com/kcp-dev/virtual-workspace-framework/pkg/impersonation instead.
// This wrapper will be removed in a future release.
func HasImpersonationHeaders(header http.Header) bool {
	return impersonation.HasHeaders(header)
}

// ScrubImpersonationHeaders removes every impersonation header from the request.
//
// Deprecated: use impersonation.ScrubHeaders from
// github.com/kcp-dev/virtual-workspace-framework/pkg/impersonation instead.
// This wrapper will be removed in a future release.
func ScrubImpersonationHeaders(header http.Header) {
	impersonation.ScrubHeaders(header)
}
