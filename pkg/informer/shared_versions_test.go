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

package informer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"

	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"
)

type fakeGVRSource struct {
	gvrs map[schema.GroupVersionResource]GVRPartialMetadata
}

func (s *fakeGVRSource) GVRs() map[schema.GroupVersionResource]GVRPartialMetadata {
	return s.gvrs
}

func (s *fakeGVRSource) Ready() bool {
	return true
}

func (s *fakeGVRSource) Subscribe() <-chan struct{} {
	return make(chan struct{})
}

type fakeMetadataClient struct {
	lock      sync.Mutex
	requested []schema.GroupVersionResource
}

func (c *fakeMetadataClient) Cluster(logicalcluster.Path) metadata.Interface { return nil }

func (c *fakeMetadataClient) Resource(gvr schema.GroupVersionResource) kcpmetadata.ResourceClusterInterface {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.requested = append(c.requested, gvr)
	return &fakeMetadataResourceClient{}
}

func (c *fakeMetadataClient) requestedVersions() []string {
	c.lock.Lock()
	defer c.lock.Unlock()
	versions := make([]string, 0, len(c.requested))
	for _, gvr := range c.requested {
		versions = append(versions, gvr.Version)
	}
	return versions
}

type fakeMetadataResourceClient struct{}

func (c *fakeMetadataResourceClient) Cluster(logicalcluster.Path) metadata.Getter { return nil }

func (c *fakeMetadataResourceClient) List(context.Context, metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	return &metav1.PartialObjectMetadataList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}}, nil
}

func (c *fakeMetadataResourceClient) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return watch.NewFake(), nil
}

func TestPartialMetadataInformerSharedAcrossVersions(t *testing.T) {
	t.Parallel()

	v1 := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	v2 := v1.GroupResource().WithVersion("v2")
	widget := withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "Widget", "widget")

	source := &fakeGVRSource{gvrs: map[schema.GroupVersionResource]GVRPartialMetadata{v1: widget, v2: widget}}
	client := &fakeMetadataClient{}
	f, err := NewDiscoveringPartialMetadataSharedInformerFactory(client, nil, func(any) bool { return false }, nil, source, cache.Indexers{})
	require.NoError(t, err)

	f.updateInformers()

	inf1, err := f.ForResource(v1)
	require.NoError(t, err)
	inf2, err := f.ForResource(v2)
	require.NoError(t, err)
	require.Same(t, inf1, inf2)
	require.Len(t, f.informerStops, 1)

	require.Eventually(t, func() bool { return len(client.requestedVersions()) > 0 }, 10*time.Second, 10*time.Millisecond)
	require.Contains(t, []string{"v1", "v2"}, client.requestedVersions()[0])

	source.gvrs = map[schema.GroupVersionResource]GVRPartialMetadata{v1: widget}
	f.updateInformers()

	inf, err := f.ForResource(v1)
	require.NoError(t, err)
	require.Same(t, inf1, inf)
	require.Len(t, f.informerStops, 1)
	require.Equal(t, v1, f.servedGVR(v2))

	source.gvrs = map[schema.GroupVersionResource]GVRPartialMetadata{}
	f.updateInformers()

	require.Empty(t, f.informers)
	require.Empty(t, f.informerStops)
	require.Empty(t, f.startedInformers)
}
