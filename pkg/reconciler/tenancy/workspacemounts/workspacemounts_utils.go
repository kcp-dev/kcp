/*
Copyright 2024 The KCP Authors.

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

package workspacemounts

import (
	"errors"
	"strings"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
)

// getWorkspaceKey return key for the workspace object.
func getWorkspaceKey(obj interface{}) (string, error) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return "", err
	}
	key = workspaceKeyPrefix + key
	return key, nil
}

// getWorkspaceKeyFromCluster returns key for the workspace object.
func getWorkspaceKeyFromCluster(cluster logicalcluster.Name, name string) string {
	return workspaceKeyPrefix + kcpcache.ToClusterAwareKey(cluster.String(), "", name)
}

// getGVKKey returns a key for the GVK object so we can easily resolve it into GVK using schema.ParseResourceArg
// Format: prefix::gvr.resource.gvr.version.gvr.group::cluster|namespace/name.
func getGVKKey(gvr schema.GroupVersionResource, obj interface{}) (string, error) {
	gvkKey := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".")

	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return "", err
	}
	key = gvrKeyPrefix + gvkKey + "::" + key
	return key, nil
}

func parseWorkspaceKey(key string) (logicalcluster.Name, string, error) {
	if !strings.HasPrefix(key, workspaceKeyPrefix) {
		return "", "", errors.New("unexpected key format")
	}
	key = strings.TrimPrefix(key, workspaceKeyPrefix)

	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return "", "", err
	}
	return cluster, name, nil
}

func parseGVKKey(key string) (schema.GroupVersionResource, string, error) {
	if !strings.HasPrefix(key, gvrKeyPrefix) {
		return schema.GroupVersionResource{}, "", errors.New("unexpected key format")
	}
	key = strings.TrimPrefix(key, gvrKeyPrefix)

	parts := strings.SplitN(key, "::", 2)
	if len(parts) != 2 {
		return schema.GroupVersionResource{}, "", errors.New("unexpected key format")
	}

	gvr, _ := schema.ParseResourceArg(parts[0])
	if gvr == nil {
		return schema.GroupVersionResource{}, "", errors.New("unable to parse gvr string")
	}

	return *gvr, parts[1], nil
}
