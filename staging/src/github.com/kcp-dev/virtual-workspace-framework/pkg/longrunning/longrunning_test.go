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

package longrunning

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
)

func TestWithStreamingCustomSubresources(t *testing.T) {
	base := genericfilters.BasicLongRunningRequestCheck(
		sets.NewString("watch", "proxy"),
		sets.NewString("attach", "exec", "proxy", "log", "portforward"),
	)

	tests := map[string]struct {
		info    *apirequest.RequestInfo
		upgrade bool
		want    bool
	}{
		"upgrade on a custom subresource": {
			info:    &apirequest.RequestInfo{IsResourceRequest: true, Verb: "get", Resource: "linuxservers", Subresource: "ssh"},
			upgrade: true,
			want:    true,
		},
		"plain call to a custom subresource": {
			info: &apirequest.RequestInfo{IsResourceRequest: true, Verb: "get", Resource: "linuxservers", Subresource: "ssh"},
			want: false,
		},
		"upgrade on the resource itself": {
			info:    &apirequest.RequestInfo{IsResourceRequest: true, Verb: "get", Resource: "linuxservers"},
			upgrade: true,
			want:    false,
		},
		"upgrade on status": {
			info:    &apirequest.RequestInfo{IsResourceRequest: true, Verb: "get", Resource: "linuxservers", Subresource: "status"},
			upgrade: true,
			want:    false,
		},
		"upgrade on scale": {
			info:    &apirequest.RequestInfo{IsResourceRequest: true, Verb: "get", Resource: "linuxservers", Subresource: "scale"},
			upgrade: true,
			want:    false,
		},
		"upgrade on a non-resource path": {
			info:    &apirequest.RequestInfo{IsResourceRequest: false, Verb: "get", Path: "/healthz"},
			upgrade: true,
			want:    false,
		},
		"base check still applies: watch": {
			info: &apirequest.RequestInfo{IsResourceRequest: true, Verb: "watch", Resource: "linuxservers"},
			want: true,
		},
		"base check still applies: exec without upgrade": {
			info: &apirequest.RequestInfo{IsResourceRequest: true, Verb: "create", Resource: "pods", Subresource: "exec"},
			want: true,
		},
		"no request info": {
			upgrade: true,
			want:    false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.upgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			check := WithStreamingCustomSubresources(base)
			if tt.info == nil {
				check = WithStreamingCustomSubresources(nil)
			}
			if got := check(req, tt.info); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
