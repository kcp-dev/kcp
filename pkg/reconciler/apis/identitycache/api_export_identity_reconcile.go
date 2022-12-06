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

package identitycache

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configshard "github.com/kcp-dev/kcp/config/shard"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func (c *controller) reconcile(ctx context.Context) error {
	apiExports, err := c.listAPIExportsFromRemoteShard(tenancyv1alpha1.RootCluster)
	if err != nil {
		return err
	}
	requiredApiExportIdentitiesConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      ConfigMapName,
		},
		Data: map[string]string{},
	}
	for _, apiExport := range apiExports {
		if apiExport.Status.IdentityHash == "" {
			return nil // we cannot do anything here, we will get notified when an identity is assigned.
		}
		requiredApiExportIdentitiesConfigMap.Data[apiExport.Name] = apiExport.Status.IdentityHash
	}

	apiExportIdentitiesConfigMap, err := c.getConfigMap(configshard.SystemShardCluster, "default", ConfigMapName)
	if apierrors.IsNotFound(err) {
		_, err := c.createConfigMap(ctx, configshard.SystemShardCluster.Path(), "default", requiredApiExportIdentitiesConfigMap)
		return err
	}
	if err != nil {
		return err
	}
	if !equality.Semantic.DeepEqual(apiExportIdentitiesConfigMap.Data, requiredApiExportIdentitiesConfigMap.Data) {
		toUpdateResourceIdentitiesConfigMap := apiExportIdentitiesConfigMap.DeepCopy()
		toUpdateResourceIdentitiesConfigMap.Data = requiredApiExportIdentitiesConfigMap.Data
		_, err := c.updateConfigMap(ctx, configshard.SystemShardCluster.Path(), "default", toUpdateResourceIdentitiesConfigMap)
		return err
	}
	return nil
}
