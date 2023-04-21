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

package client

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"

	clientshard "github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

var (
	// matches shards/name/remainder, capturing name.
	//
	// Example: shards/name/remainder
	// Example: /shards/name/remainder
	// Example: prefix/shards/name/remainder.
	shardNameRegex = regexp.MustCompile(`shards/([^/]+)/.+`)
)

// WithShardNameFromContextRoundTripper wraps an existing config's with ShardRoundTripper.
//
// Note: it is the caller responsibility to make a copy of the rest config.
func WithShardNameFromContextRoundTripper(cfg *rest.Config) *rest.Config {
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return NewShardRoundTripper(rt)
	})

	return cfg
}

// ShardRoundTripper is a shard aware wrapper around http.RoundTripper.
// It changes the URL path to target a shard from the context.
//
// For example given "amber" shard name in the context it will change
// apis/apis.kcp.io/v1alpha1/apiexports to /shards/amber/apis/apis.kcp.io/v1alpha1/apiexports.
type ShardRoundTripper struct {
	delegate http.RoundTripper
}

// NewShardRoundTripper creates a new shard aware round tripper.
func NewShardRoundTripper(delegate http.RoundTripper) *ShardRoundTripper {
	return &ShardRoundTripper{
		delegate: delegate,
	}
}

func (c *ShardRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	shard := ShardFromContext(req.Context())
	if !shard.Empty() {
		req = req.Clone(req.Context())
		path, err := generatePath(req.URL.Path, shard)
		if err != nil {
			return nil, err
		}
		req.URL.Path = path

		rawPath, err := generatePath(req.URL.RawPath, shard)
		if err != nil {
			return nil, err
		}
		req.URL.RawPath = rawPath
	}
	return c.delegate.RoundTrip(req)
}

// generatePath formats the request path to target the specified shard.
func generatePath(originalPath string, shard clientshard.Name) (string, error) {
	// if the originalPath already has the shard then the path was already modified and no change needed
	if strings.HasPrefix(originalPath, shard.Path()) {
		return originalPath, nil
	}
	// if the originalPath already has a shard set just overwrite it to the given one
	if strings.HasPrefix(originalPath, "/shards") {
		matches := shardNameRegex.FindStringSubmatch(originalPath)
		if len(matches) >= 2 {
			// replace /shards/$oldName/reminder with  /shards/$newName/reminder
			return strings.Replace(originalPath, clientshard.New(matches[1]).Path(), shard.Path(), 1), nil
		} else {
			// the path is either /shards/name/ or /shards/name
			path := shard.Path()
			if originalPath[len(originalPath)-1] == '/' {
				path += "/"
			}
			return path, nil
		}
	}

	// otherwise the path doesn't contain a shard name prepend /shards/name
	path := shard.Path()
	// if the original path is relative, add a / separator
	if len(originalPath) > 0 && originalPath[0] != '/' {
		path += "/"
	}
	// finally append the original path
	path += originalPath
	return path, nil
}

// WithDefaultShardRoundTripper wraps an existing config's with DefaultShardRoundTripper
//
// Note: it is the caller responsibility to make a copy of the rest config.
func WithDefaultShardRoundTripper(cfg *rest.Config, shard clientshard.Name) *rest.Config {
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return NewDefaultShardRoundTripper(rt, shard)
	})
	return cfg
}

// DefaultShardRoundTripper is a http.RoundTripper that sets a default shard name if not specified in the context.
type DefaultShardRoundTripper struct {
	delegate http.RoundTripper
	shard    clientshard.Name
}

// NewDefaultShardRoundTripper creates a new round tripper that sets a default shard name.
func NewDefaultShardRoundTripper(delegate http.RoundTripper, shard clientshard.Name) *DefaultShardRoundTripper {
	return &DefaultShardRoundTripper{
		delegate: delegate,
		shard:    shard,
	}
}

func (c *DefaultShardRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if ShardFromContext(req.Context()).Empty() {
		req = req.WithContext(WithShardInContext(req.Context(), c.shard))
	}
	return c.delegate.RoundTrip(req)
}

// WithShardNameFromObjectRoundTripper wraps an existing config with ShardNameFromObjectRoundTripper.
//
// Note: it is the caller responsibility to make a copy of the rest config.
func WithShardNameFromObjectRoundTripper(cfg *rest.Config, requestInfoResolver func(*http.Request) (string, string, error), supportedResources ...string) *rest.Config {
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return NewShardNameFromObjectRoundTripper(rt, requestInfoResolver, supportedResources...)
	})

	return cfg
}

// NewShardNameFromObjectRoundTripper creates a new ShardNameFromObjectRoundTripper for the given resources.
func NewShardNameFromObjectRoundTripper(delegate http.RoundTripper, requestInfoResolver func(*http.Request) (string, string, error), supportedResources ...string) *ShardNameFromObjectRoundTripper {
	return &ShardNameFromObjectRoundTripper{
		delegate:            delegate,
		requestInfoResolver: requestInfoResolver,
		supportedResources:  sets.New[string](supportedResources...),
		supportedVerbs:      sets.New[string]("create", "update", "patch"),
	}
}

// ShardNameFromObjectRoundTripper knows how to read a shard name
// from an object and store it in the context.
// it should be only used by the internal kube-clients that are not aware of a shard name.
type ShardNameFromObjectRoundTripper struct {
	delegate            http.RoundTripper
	requestInfoResolver func(*http.Request) (string, string, error) /*res, verb, err*/
	supportedResources  sets.Set[string]
	supportedVerbs      sets.Set[string]
}

func (c *ShardNameFromObjectRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// no-op if we already have a shard name in the cxt
	if shard := ShardFromContext(req.Context()); !shard.Empty() {
		return c.delegate.RoundTrip(req)
	}

	resource, verb, err := c.requestInfoResolver(req)
	if err != nil {
		return nil, err
	}
	if !c.supportedResources.Has(resource) || !c.supportedVerbs.Has(verb) {
		return c.delegate.RoundTrip(req)
	}

	bodyReader, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer bodyReader.Close()
	rawBody := new(bytes.Buffer)
	_, err = rawBody.ReadFrom(bodyReader)
	if err != nil {
		return nil, err
	}
	rawObject := rawBody.Bytes()
	unstructuredObject, _, err := unstructured.UnstructuredJSONScheme.Decode(rawObject, nil, nil)
	if err != nil {
		return nil, err
	}
	if object, ok := unstructuredObject.(metav1.Object); ok {
		annotations := object.GetAnnotations()
		if shardName, ok := annotations[clientshard.AnnotationKey]; ok {
			req = req.WithContext(WithShardInContext(req.Context(), clientshard.New(shardName)))
		}
	}

	return c.delegate.RoundTrip(req)
}

// WithCacheServiceRoundTripper wraps an existing config's with CacheServiceRoundTripper.
func WithCacheServiceRoundTripper(cfg *rest.Config) *rest.Config {
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return NewCacheServiceRoundTripper(rt)
	})
	return cfg
}

// CacheServiceRoundTripper is a http.RoundTripper that appends "/services/cache" prefix to a request.
type CacheServiceRoundTripper struct {
	delegate http.RoundTripper
}

// NewCacheServiceRoundTripper creates a new CacheServiceRoundTripper.
func NewCacheServiceRoundTripper(delegate http.RoundTripper) *CacheServiceRoundTripper {
	return &CacheServiceRoundTripper{
		delegate: delegate,
	}
}

func (c *CacheServiceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cacheServicePrefix := "/services/cache"
	if !strings.HasPrefix(req.URL.Path, cacheServicePrefix) {
		req = req.Clone(req.Context())
		// if the original path is relative, add a / separator
		if len(req.URL.Path) > 0 && req.URL.Path[0] != '/' {
			cacheServicePrefix += "/"
		}
		// now simply append the cache service prefix to original path
		// and regenerate the URL address so that the RawPath gets updated as well
		req.URL.Path = fmt.Sprintf("%s%s", cacheServicePrefix, req.URL.Path)
		newURL, err := url.Parse(req.URL.String())
		if err != nil {
			return nil, err
		}
		req.URL = newURL
	}
	return c.delegate.RoundTrip(req)
}
