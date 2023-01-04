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

package metadata

import (
	"fmt"
	"net/http"
	"strings"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	dynamic "k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
)

// NewDynamicMetadataClusterClientForConfig returns a dynamic cluster client that only
// retrieves PartialObjectMetadata-like object, returned as Unstructured.
func NewDynamicMetadataClusterClientForConfig(config *rest.Config) (kcpdynamic.ClusterInterface, error) {
	// create special client that only gets PartialObjectMetadata objects. For these we can do
	// wildcard requests with different schemas without risking data loss.
	metadataConfig := *config
	metadataConfig.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		// we have to use this way because the dynamic client overrides the content-type :-/
		return &metadataTransport{RoundTripper: rt}
	})
	return kcpdynamic.NewForConfig(&metadataConfig)
}

// NewDynamicMetadataClientForConfig returns a dynamic client that only
// retrieves PartialObjectMetadata-like object, returned as Unstructured.
func NewDynamicMetadataClientForConfig(config *rest.Config) (dynamic.Interface, error) {
	// create special client that only gets PartialObjectMetadata objects. For these we can do
	// wildcard requests with different schemas without risking data loss.
	metadataConfig := *config
	metadataConfig.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		// we have to use this way because the dynamic client overrides the content-type :-/
		return &metadataTransport{RoundTripper: rt}
	})
	return dynamic.NewForConfig(&metadataConfig)
}

// metadataTransport does what client-go/metadata does, but injected into a dynamic client
// that is expected by the dynamic informers.
type metadataTransport struct {
	http.RoundTripper
}

func (t *metadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	partialType, err := partialType(req)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", fmt.Sprintf("application/json;as=%s;g=meta.k8s.io;v=v1", partialType))
	return t.RoundTripper.RoundTrip(req)
}

func partialType(req *http.Request) (string, error) {
	// strip off /cluster/<lcluster>
	baseReq := *req
	if strings.HasPrefix(req.URL.Path, "/clusters/") {
		parts := strings.SplitN(req.URL.Path, "/", 4)
		if len(parts) < 4 {
			return "", fmt.Errorf("invalid request uri: %s", req.URL.String())
		}
		baseURL := *req.URL
		baseURL.Path = "/" + parts[3]
		baseReq.URL = &baseURL
	}

	info, err := requestinfo.NewFactory().NewRequestInfo(&baseReq)
	if err != nil {
		return "", err
	}
	switch info.Verb {
	case "list":
		return "PartialObjectMetadataList", nil
	case "watch", "get":
		return "PartialObjectMetadata", nil
	}

	return "", fmt.Errorf("unexpected verb %q for metadata client", info.Verb)
}
