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

package endpointslice

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type DeserializeErrorCode int

const (
	NoEndpoints DeserializeErrorCode = iota
	BadObject
)

type DeserializeError struct {
	Code DeserializeErrorCode
	Err  error
}

func (e *DeserializeError) Error() string {
	return e.Err.Error()
}

// ListURLsFromUnstructured retrieves list of endpoint URLs from an unstructured object.
// The URLs are expected to be present at `.status.endpoints[].url` path inside the object.
func ListURLsFromUnstructured(endpointSlice unstructured.Unstructured) ([]string, error) {
	statusRaw, found, err := unstructured.NestedFieldNoCopy(endpointSlice.Object, "status")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &DeserializeError{
			Code: NoEndpoints,
			Err:  fmt.Errorf("missing status"),
		}
	}
	status, ok := statusRaw.(map[string]interface{})
	if !ok {
		return nil, &DeserializeError{
			Code: BadObject,
			Err:  fmt.Errorf("status field is of type %T, expected map[string]interface{}", statusRaw),
		}
	}

	endpointsRaw, found, err := unstructured.NestedFieldNoCopy(status, "endpoints")
	if err != nil {
		return nil, err
	}
	if !found || endpointsRaw == nil {
		return nil, &DeserializeError{
			Code: NoEndpoints,
			Err:  fmt.Errorf("missing status.endpoints"),
		}
	}
	endpoints, ok := endpointsRaw.([]interface{})
	if !ok {
		return nil, &DeserializeError{
			Code: BadObject,
			Err:  fmt.Errorf("status.endpoints field is of type %T, expected map[string]interface{}", statusRaw),
		}
	}

	urls := make([]string, 0, len(endpoints))
	for i, ep := range endpoints {
		endpointMap, ok := ep.(map[string]interface{})
		if !ok {
			return nil, &DeserializeError{
				Code: BadObject,
				Err:  fmt.Errorf("endpoint at index %d is not an object", i),
			}
		}

		url, found, err := unstructured.NestedString(endpointMap, "url")
		if err != nil {
			return nil, fmt.Errorf("failed to get url from endpoint at index %d: %w", i, err)
		}
		if !found {
			return nil, &DeserializeError{
				Code: BadObject,
				Err:  fmt.Errorf("missing url in endpoint at index %d", i),
			}
		}

		urls = append(urls, url)
	}

	return urls, nil
}

// FindOneURL finds exactly one URL with matching prefix in the urls slice.
// Multiple matches result in an error.
func FindOneURL(prefix string, urls []string) (string, error) {
	var matches []string
	for _, url := range urls {
		if strings.HasPrefix(url, prefix) {
			matches = append(matches, url)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no URLs match prefix %q", prefix)
	default:
		return "", fmt.Errorf("ambiguous URLs %v with prefix %q", matches, prefix)
	}
}
