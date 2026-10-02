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

package conversion

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/conversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/sdk/apis/apis"
)

func TestNewCRConverterFactory(t *testing.T) {
	t.Parallel()

	f, err := NewCRConverterFactory(nil, wait.ForeverTestTimeout, true)
	require.NoError(t, err)

	t.Run("apis.kcp.io uses schema converter", func(t *testing.T) {
		t.Parallel()
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: apis.GroupName,
			},
		}
		converter, err := f.NewConverter(crd)
		require.NoError(t, err)
		require.IsType(t, &schemaBasedConverter{}, converter)
	})

	t.Run("partial metadata uses nop converter", func(t *testing.T) {
		t.Parallel()
		crd := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				UID: types.UID("foo.wildcard.partial-metadata"),
			},
		}
		converter, err := f.NewConverter(crd)
		require.NoError(t, err)
		require.IsType(t, conversion.NewNOPConverter(), converter)
	})

	t.Run("other CRDs use deferred converter", func(t *testing.T) {
		t.Parallel()
		crd := &apiextensionsv1.CustomResourceDefinition{}
		converter, err := f.NewConverter(crd)
		require.NoError(t, err)
		require.IsType(t, &deferredConverter{}, converter)
	})

	t.Run("apis.kcp.io still converts when feature gate disabled", func(t *testing.T) {
		t.Parallel()
		disabled, err := NewCRConverterFactory(nil, wait.ForeverTestTimeout, false)
		require.NoError(t, err)
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: apis.GroupName,
			},
		}
		converter, err := disabled.NewConverter(crd)
		require.NoError(t, err)
		require.IsType(t, &schemaBasedConverter{}, converter)
	})
}
