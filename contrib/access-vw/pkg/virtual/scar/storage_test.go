package scar

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	accessv1alpha1 "github.com/kcp-dev/kcp/contrib/access-vw/pkg/apis/access/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
)

func TestCreate_GraphNotReady(t *testing.T) {
	t.Parallel()

	g := graph.New() // not ready
	r := NewREST(g)

	ctx := genericapirequest.WithUser(context.Background(), &user.DefaultInfo{Name: "alice"})
	_, err := r.Create(ctx, &accessv1alpha1.SelfClusterAccessReview{}, nil, nil)
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("expected ServiceUnavailable, got %v", err)
	}
}

func TestCreate_NoUser(t *testing.T) {
	t.Parallel()

	g := graph.New()
	g.SetReady()
	r := NewREST(g)

	_, err := r.Create(context.Background(), &accessv1alpha1.SelfClusterAccessReview{}, nil, nil)
	if !apierrors.IsUnauthorized(err) {
		t.Fatalf("expected Unauthorized, got %v", err)
	}
}

func TestCreate_ReturnsCallerClusters(t *testing.T) {
	t.Parallel()

	g := graph.New()
	g.Grant(graph.User("alice"), graph.LogicalCluster("ws1"), "https://kcp.example/clusters/ws1")
	g.Grant(graph.Group("team-a"), graph.LogicalCluster("ws2"), "https://kcp.example/clusters/ws2")
	g.Grant(graph.User("bob"), graph.LogicalCluster("ws3"), "https://kcp.example/clusters/ws3")
	g.SetReady()
	r := NewREST(g)

	ctx := genericapirequest.WithUser(context.Background(), &user.DefaultInfo{
		Name:   "alice",
		Groups: []string{"team-a"},
	})
	obj, err := r.Create(ctx, &accessv1alpha1.SelfClusterAccessReview{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	review := obj.(*accessv1alpha1.SelfClusterAccessReview)
	if got := len(review.Status.Clusters); got != 2 {
		t.Fatalf("expected 2 clusters (user + group grant), got %d: %+v", got, review.Status.Clusters)
	}
	names := map[string]bool{}
	for _, c := range review.Status.Clusters {
		names[c.ClusterName] = true
	}
	if !names["ws1"] || !names["ws2"] {
		t.Errorf("expected ws1 and ws2, got %+v", review.Status.Clusters)
	}
	if names["ws3"] {
		t.Errorf("bob's ws3 leaked into alice's result: %+v", review.Status.Clusters)
	}
}
