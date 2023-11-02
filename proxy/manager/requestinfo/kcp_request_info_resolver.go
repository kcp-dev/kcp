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

package requestinfo

import (
	"net/http"
	"regexp"

	"k8s.io/apiserver/pkg/endpoints/request"
)

type KCPRequestInfoResolver struct {
	delegate request.RequestInfoResolver
}

func NewKCPRequestInfoResolver() *KCPRequestInfoResolver {
	return &KCPRequestInfoResolver{
		delegate: NewFactory(),
	}
}

var clustersRE = regexp.MustCompile(`/clusters/[^/]+/(.*)$`)

func (k *KCPRequestInfoResolver) NewRequestInfo(req *http.Request) (*request.RequestInfo, error) {
	matches := clustersRE.FindStringSubmatch(req.URL.Path)
	if len(matches) == 2 {
		// matches[0] is the leftmost pattern that matches (which includes /clusters/...) - skip over that
		strippedURL := matches[1]
		t := req.Clone(req.Context())
		t.URL.Path = strippedURL
		return k.delegate.NewRequestInfo(t)
	}
	return k.delegate.NewRequestInfo(req)
}
