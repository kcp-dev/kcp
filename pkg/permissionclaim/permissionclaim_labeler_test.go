/*
Copyright 2023 The KCP Authors.

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

package permissionclaim

import (
	"testing"

	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestIsSelected(t *testing.T) {
	type args struct {
		claim     v1alpha1.AcceptablePermissionClaim
		name      string
		namespace string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "All resources selected",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						All: true,
					},
				},
				name:      "",
				namespace: "",
			},
			want: true,
		},
		{
			name: "Only name selected",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Names: []string{"John Wayne"},
							},
						},
					},
				},
				name:      "John Wayne",
				namespace: "",
			},
			want: true,
		},
		{
			name: "Only namespace selected",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Namespaces: []string{"foo"},
							},
						},
					},
				},
				name:      "",
				namespace: "foo",
			},
			want: true,
		},
		{
			name: "Name selection doesn't match",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Names:      []string{"John Wayne"},
								Namespaces: []string{"foo"},
							},
						},
					},
				},
				name:      "John Newman",
				namespace: "foo",
			},
			want: false,
		},
		{
			name: "Namespace selection doesn't match",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Names:      []string{"John Wayne"},
								Namespaces: []string{"foo"},
							},
						},
					},
				},
				name:      "John Wayne",
				namespace: "bar",
			},
			want: false,
		},
		{
			name: "No selection criteria specified, select all",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
					},
				},
				name:      "",
				namespace: "",
			},
			want: true,
		},
		{
			name: "All and selector specified",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						All: true,
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Names:      []string{"John"},
								Namespaces: []string{"foo"},
							},
						},
					},
				},
				name:      "John",
				namespace: "foo",
			},
			want: false,
		},
		{
			name: "All namespaces selected",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Names:      []string{"John"},
								Namespaces: []string{"*"},
							},
						},
					},
				},
				name:      "John",
				namespace: "bar",
			},
			want: true,
		},
		{
			name: "All names selected",
			args: args{
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Names:      []string{"*"},
								Namespaces: []string{"bar"},
							},
						},
					},
				},
				name:      "Jim",
				namespace: "bar",
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Selects(tt.args.claim, tt.args.name, tt.args.namespace); got != tt.want {
				t.Errorf("isSelected() = %v, want %v", got, tt.want)
			}
		})
	}
}
