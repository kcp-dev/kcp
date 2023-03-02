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
