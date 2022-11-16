package permissionclaim

import (
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"testing"
)

func TestIsSelected(t *testing.T) {
	type args struct {
		claim     v1alpha1.PermissionClaim
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
				claim: v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    "kcp.io",
						Resource: "cowboys",
					},
					All: true,
				},
				name:      "",
				namespace: "",
			},
			want: true,
		},
		{
			name: "Only name selected",
			args: args{
				claim: v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    "kcp.io",
						Resource: "cowboys",
					},
					ResourceSelector: []v1alpha1.ResourceSelector{
						{
							Name:      "John Wayne",
							Namespace: "",
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
				claim: v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    "kcp.io",
						Resource: "cowboys",
					},
					ResourceSelector: []v1alpha1.ResourceSelector{
						{
							Name:      "",
							Namespace: "foo",
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
				claim: v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    "kcp.io",
						Resource: "cowboys",
					},
					ResourceSelector: []v1alpha1.ResourceSelector{
						{
							Name:      "John Wayne",
							Namespace: "foo",
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
				claim: v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    "kcp.io",
						Resource: "cowboys",
					},
					ResourceSelector: []v1alpha1.ResourceSelector{
						{
							Name:      "John Wayne",
							Namespace: "foo",
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
				claim: v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    "kcp.io",
						Resource: "cowboys",
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
				claim: v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    "kcp.io",
						Resource: "cowboys",
					},
					All: true,
					ResourceSelector: []v1alpha1.ResourceSelector{
						{
							Name:      "John",
							Namespace: "foo",
						},
					},
				},
				name:      "John",
				namespace: "foo",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSelected(tt.args.claim, tt.args.name, tt.args.namespace); got != tt.want {
				t.Errorf("isSelected() = %v, want %v", got, tt.want)
			}
		})
	}
}
