package registry

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func TestWorkspaceStrategy(t *testing.T) {
	ctx := apirequest.NewDefaultContext()
	if Strategy.NamespaceScoped() {
		t.Errorf("Workspaces should not be namespace scoped")
	}
	if Strategy.AllowCreateOnUpdate() {
		t.Errorf("Workspaces should not allow create on update")
	}
	if Strategy.AllowUnconditionalUpdate() {
		t.Errorf("Workspaces should not allow unconditional update")
	}
	project := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "foo", ResourceVersion: "10"},
	}
	Strategy.PrepareForCreate(ctx, project)
	errs := Strategy.Validate(ctx, project)
	if len(errs) != 0 {
		t.Errorf("Unexpected error validating %v", errs)
	}
}
