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

package plugin

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcpscheme "github.com/kcp-dev/sdk/client/clientset/versioned/scheme"
)

func init() {
	// The metav1.TableXXX types (that are in the metav1 scheme) are not added by default
	// to the generated KCP clientset scheme.
	// So when we want to get the result of a request done with this clientset as a table,
	// it doesn't know the Table types and returns an error.
	//
	// As a comparison, it happens that in the kubectl code, those types are added to the main
	// kubectl scheme.
	if err := metav1.AddMetaToScheme(kcpscheme.Scheme); err != nil {
		panic(err)
	}
}
