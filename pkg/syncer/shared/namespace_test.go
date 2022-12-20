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

package shared

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestLocatorFromAnnotations(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        *NamespaceLocator
		wantFound   bool
		wantErrs    []string
	}{
		{
			name:      "no annotation",
			wantFound: false,
		},
		{
			name: "garbage",
			annotations: map[string]string{
				NamespaceLocatorAnnotation: "garbage",
			},
			wantErrs: []string{"invalid character"},
		},
		{
			name: "happy case",
			annotations: map[string]string{
				NamespaceLocatorAnnotation: `{"syncTarget":{"cluster":"test-workspace","name":"test-name","uid":"test-uid"},"cluster":"test-workspace","namespace":"test-namespace"}`,
			},
			want: &NamespaceLocator{
				SyncTarget: SyncTargetLocator{
					ClusterName: "test-workspace",
					Name:        "test-name",
					UID:         "test-uid",
				},
				ClusterName: logicalcluster.Name("test-workspace"),
				Namespace:   "test-namespace",
			},
			wantFound: true,
		},
		{
			name: "format up to v0.6.0",
			annotations: map[string]string{
				NamespaceLocatorAnnotation: `{"syncTarget":{"path":"test-workspace","name":"test-name","uid":"test-uid"},"cluster":"test-workspace","namespace":"test-namespace"}`,
			},
			want: &NamespaceLocator{
				SyncTarget: SyncTargetLocator{
					ClusterName: "test-workspace",
					Name:        "test-name",
					UID:         "test-uid",
				},
				ClusterName: logicalcluster.Name("test-workspace"),
				Namespace:   "test-namespace",
			},
			wantFound: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotFound, err := LocatorFromAnnotations(tt.annotations)
			if (err != nil) != (len(tt.wantErrs) > 0) {
				t.Errorf("LocatorFromAnnotations() error = %q, wantErrs %v", err.Error(), tt.wantErrs)
				return
			} else if err != nil {
				for _, wantErr := range tt.wantErrs {
					if !strings.Contains(err.Error(), wantErr) {
						t.Errorf("LocatorFromAnnotations() error = %q, wantErrs %q", err.Error(), wantErr)
						return
					}
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LocatorFromAnnotations() got = %v, want %v", got, tt.want)
			}
			if gotFound != tt.wantFound {
				t.Errorf("LocatorFromAnnotations() gotFound = %v, want %v", gotFound, tt.wantFound)
			}
		})
	}
}
