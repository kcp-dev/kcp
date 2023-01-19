package permissionclaim

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestIsSelected(t *testing.T) {
	type args struct {
		groupResource schema.GroupResource
		claim         v1alpha1.AcceptablePermissionClaim
		name          string
		namespace     string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "All resources selected",
			args: args{
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
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
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Name: "John Wayne",
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
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Name:       "kcp.io",
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
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Name:       "John Wayne",
								Namespaces: []string{"foo"},
							},
						},
					},
				},
				name:      "John Newman",
				namespace: "foo",
			},
			want: true,
		},
		{
			name: "Namespace selection doesn't match",
			args: args{
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "kcp.io",
							Resource: "cowboys",
						},
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Name:       "John Wayne",
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
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
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
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
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
								Name:       "John",
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
			name: "GroupResource are not selected",
			args: args{
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "",
							Resource: "configmap",
						},
						All: true,
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Name:       "John",
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
			name: "Resource selected not group",
			args: args{
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimAccepted,
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    "",
							Resource: "cowboys",
						},
						All: true,
						ResourceSelector: []v1alpha1.ResourceSelector{
							{
								Name:       "John",
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
			name: "No selection criteria specified, claim not accepted",
			args: args{
				groupResource: schema.GroupResource{
					Group:    "kcp.io",
					Resource: "cowboys",
				},
				claim: v1alpha1.AcceptablePermissionClaim{
					State: v1alpha1.ClaimRejected,
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
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Selects(tt.args.claim, tt.args.groupResource, tt.args.name, tt.args.namespace); got != tt.want {
				t.Errorf("isSelected() = %v, want %v", got, tt.want)
			}
		})
	}
}
