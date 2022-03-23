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

package sheriffs

import (
	"context"
	"embed"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configcrds "github.com/kcp-dev/kcp/config/crds"
)

//go:embed *.yaml
var rawCustomResourceDefinitions embed.FS

func Create(t *testing.T, client apiextensionsv1client.CustomResourceDefinitionInterface, grs ...metav1.GroupResource) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	err := configcrds.CreateFromFS(ctx, client, rawCustomResourceDefinitions, grs...)
	require.NoError(t, err)
}
