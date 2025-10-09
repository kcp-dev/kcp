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

package context

import (
	"context"
)

type virtualResourceAPIExportIdentityKeyType string

// virtualResourceAPIExportIdentityKey is a context key that contains the APIExport identity
// of an exported virtual resource.
const virtualResourceAPIExportIdentityKey virtualResourceAPIExportIdentityKeyType = "VirtualResourceAPIExportIdentity"

// WithVirtualResourceAPIExportIdentity adds the APIExport identity to the context.
func WithVirtualResourceAPIExportIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, virtualResourceAPIExportIdentityKey, identity)
}

// VirtualResourceAPIExportIdentityFrom retrieves the APIExport identity from the context, if any.
func VirtualResourceAPIExportIdentityFrom(ctx context.Context) (string, bool) {
	identity, hasIdentity := ctx.Value(virtualResourceAPIExportIdentityKey).(string)
	return identity, hasIdentity
}
