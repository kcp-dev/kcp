/*
Copyright 2023 The KCP Authors.

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
	"errors"
)

// proxyTargetNameContextKeyType is the type of the key for the request context value
// that will carry the name of the Proxy resources with be proxied with.
type proxyTargetNameContextKeyType string

// proxyTargetKeyContextKey is the key for the request context value
// that will carry the name of the SyncTarget resources with be synced with.
const proxyTargetKeyContextKey proxyTargetNameContextKeyType = "ProxyAgentVirtualWorkspaceProxyTargetKey"

// WithProxyTargetKey adds a Proxy key to the context.
func WithProxyTargetKey(ctx context.Context, syncTargetKey string) context.Context {
	return context.WithValue(ctx, proxyTargetKeyContextKey, syncTargetKey)
}

// ProxyTargetKeyFrom retrieves the Proxy name key from the context, if any.
func ProxyTargetKeyFrom(ctx context.Context) (string, error) {
	stk, hasProxyTargetKey := ctx.Value(proxyTargetKeyContextKey).(string)
	if !hasProxyTargetKey {
		return "", errors.New("context must contain a valid non-empty Proxy key")
	}
	return stk, nil
}
