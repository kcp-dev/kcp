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

package filters

import (
	"context"
	"net/http"
)

type (
	userAgentContextKeyType int
)

const (
	// userAgentContextKey is the context key for the request user-agent.
	userAgentContextKey userAgentContextKeyType = iota
)

func WithUserAgent(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), userAgentContextKey, req.Header.Get("User-Agent"))
		handler.ServeHTTP(w, req.WithContext(ctx))
	})
}

func UserAgentFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(userAgentContextKey); v != nil {
		return v.(string)
	}
	return ""
}
