/*
Copyright 2025 The kcp Authors.

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
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

// ListEndpointsFromUnstructured retrieves the endpoints of an unstructured
// endpoint slice.
//
// Reference targets are read unstructured -- an APIExport may point at any kind
// -- so the shape read here is the contract carried by corev1alpha1.Endpoint,
// which APIExportEndpoint and CachedResourceEndpoint both alias: status.endpoints[]
// with a url, and optionally a shards selector saying which shards that url serves.
func ListEndpointsFromUnstructured(endpointSlice unstructured.Unstructured) ([]corev1alpha1.Endpoint, error) {
	statusRaw, found, err := unstructured.NestedFieldNoCopy(endpointSlice.Object, "status")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &DeserializeError{Code: NoEndpoints, Err: fmt.Errorf("missing status")}
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
		return nil, &DeserializeError{Code: NoEndpoints, Err: fmt.Errorf("missing status.endpoints")}
	}
	rawEndpoints, ok := endpointsRaw.([]interface{})
	if !ok {
		return nil, &DeserializeError{
			Code: BadObject,
			Err:  fmt.Errorf("status.endpoints field is of type %T, expected []interface{}", endpointsRaw),
		}
	}

	endpoints := make([]corev1alpha1.Endpoint, 0, len(rawEndpoints))
	for i, raw := range rawEndpoints {
		endpointMap, ok := raw.(map[string]interface{})
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

		endpoint := corev1alpha1.Endpoint{URL: url}

		shardsRaw, found, err := unstructured.NestedFieldNoCopy(endpointMap, "shards")
		if err != nil {
			return nil, fmt.Errorf("failed to get shards from endpoint at index %d: %w", i, err)
		}
		if found && shardsRaw != nil {
			shardsMap, ok := shardsRaw.(map[string]interface{})
			if !ok {
				return nil, &DeserializeError{
					Code: BadObject,
					Err:  fmt.Errorf("shards in endpoint at index %d is of type %T, expected an object", i, shardsRaw),
				}
			}
			shards := corev1alpha1.EndpointSelector{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(shardsMap, &shards); err != nil {
				return nil, &DeserializeError{
					Code: BadObject,
					Err:  fmt.Errorf("shards in endpoint at index %d is not a shard selector: %w", i, err),
				}
			}
			endpoint.Shards = shards
		}

		endpoints = append(endpoints, endpoint)
	}

	return endpoints, nil
}

// PickURL chooses the URL a shard with these labels should use.
//
// An endpoint that says which shards it is for is matched against those labels,
// and the most specific match wins: a slice may advertise one URL for every
// shard and override it for a region, and the region is the answer where it
// applies. Endpoints that say nothing fall back to prefix matching, which is
// how kcp's own controllers publish one URL per shard.
//
// A URL that says nothing and does not carry this shard's prefix is not adopted
// on the grounds that it is the only one there is: a lone URL reads the same
// whether it is one virtual workspace for the whole installation, another
// shard's URL, or a stale one, and forwarding to the wrong shard is not a
// guess worth making. An endpoint meant for every shard says so with matchAll.
func PickURL(prefix string, shardLabels labels.Set, endpoints []corev1alpha1.Endpoint) (string, error) {
	var (
		urls          []string
		bestURL       string
		bestSpecifity = -1
		bestCount     int
		selective     bool
	)

	for _, endpoint := range endpoints {
		urls = append(urls, endpoint.URL)
		shards := endpoint.Shards
		if !shards.MatchAll && shards.Selector == nil {
			continue
		}
		selective = true

		// matchAll is the whole installation, so anything with requirements is
		// a deliberate narrowing and beats it.
		specificity := 0
		if shards.Selector != nil {
			if shards.MatchAll {
				return "", fmt.Errorf("endpoint %q sets both matchAll and selector, which are mutually exclusive", endpoint.URL)
			}

			selector, err := metav1.LabelSelectorAsSelector(shards.Selector)
			if err != nil {
				return "", fmt.Errorf("endpoint %q has an invalid shard selector: %w", endpoint.URL, err)
			}
			if !selector.Matches(shardLabels) {
				continue
			}
			specificity = len(shards.Selector.MatchLabels) + len(shards.Selector.MatchExpressions)
		}

		switch {
		case specificity > bestSpecifity:
			bestURL, bestSpecifity, bestCount = endpoint.URL, specificity, 1
		case specificity == bestSpecifity:
			bestCount++
		}
	}

	if selective {
		switch bestCount {
		case 1:
			return bestURL, nil
		case 0:
			return "", fmt.Errorf("none of the endpoints %v select a shard labelled %v", urls, shardLabels)
		default:
			return "", fmt.Errorf("%d endpoints select a shard labelled %v equally well, out of %v", bestCount, shardLabels, urls)
		}
	}

	url, err := FindOneURL(prefix, urls)
	if err != nil {
		return "", fmt.Errorf("%w: no endpoint says which shards it serves, so a URL is this shard's only if it starts with %q; "+
			"an endpoint serving every shard should set shards.matchAll", err, prefix)
	}
	return url, nil
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
