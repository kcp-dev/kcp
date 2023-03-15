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

package crds

import (
	"context"
	"embed"
	"testing"

	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/sdk/apis/tenancy"
)

func TestCreateFromFS(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context //nolint:containedctx
		client  apiextensionsv1client.CustomResourceDefinitionInterface
		fs      embed.FS
		grs     []metav1.GroupResource
		wantErr bool
	}{
		{
			name: "context cancelled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.TODO())
				cancel()
				return ctx
			}(),
			grs: []metav1.GroupResource{
				{Group: tenancy.GroupName, Resource: "workspaces"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CreateFromFS(tt.ctx, tt.client, tt.fs, tt.grs...); (err != nil) != tt.wantErr {
				t.Errorf("CreateFromFS() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
