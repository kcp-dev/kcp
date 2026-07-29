package rbacprovider

import (
	"context"
	"fmt"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
)

const defaultResyncPeriod = 10 * time.Minute
const fallbackCluster graph.LogicalCluster = "root"

func (p *Provider) runInformers(ctx context.Context, client kubernetes.Interface, g *graph.Graph) error {
	factory := informers.NewSharedInformerFactory(client, defaultResyncPeriod)

	crbInformer := factory.Rbac().V1().ClusterRoleBindings().Informer()
	rbInformer := factory.Rbac().V1().RoleBindings().Informer()

	if _, err := crbInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { p.onCRB(obj, false) },
		UpdateFunc: func(_, obj any) { p.onCRB(obj, false) },
		DeleteFunc: func(obj any) { p.onCRB(obj, true) },
	}); err != nil {
		return fmt.Errorf("register CRB handler: %w", err)
	}

	if _, err := rbInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { p.onRB(obj, false) },
		UpdateFunc: func(_, obj any) { p.onRB(obj, false) },
		DeleteFunc: func(obj any) { p.onRB(obj, true) },
	}); err != nil {
		return fmt.Errorf("register RB handler: %w", err)
	}

	factory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(),
		crbInformer.HasSynced,
		rbInformer.HasSynced,
	) {
		return fmt.Errorf("informer cache sync failed (context cancelled or watch error)")
	}

	g.SetReady()

	<-ctx.Done()
	return nil
}

func (p *Provider) onCRB(obj any, deleted bool) {
	crb, deleted, ok := typedFromEvent[*rbacv1.ClusterRoleBinding](obj, deleted)
	if !ok {
		return
	}
	cluster := clusterOf(crb)
	if deleted {
		p.translator.RemoveClusterRoleBinding(crb.Name, cluster)
		return
	}
	p.translator.ApplyClusterRoleBinding(crb, cluster, p.endpointFor(cluster))
}

func (p *Provider) onRB(obj any, deleted bool) {
	rb, deleted, ok := typedFromEvent[*rbacv1.RoleBinding](obj, deleted)
	if !ok {
		return
	}
	cluster := clusterOf(rb)
	if deleted {
		p.translator.RemoveRoleBinding(rb.Namespace, rb.Name, cluster)
		return
	}
	p.translator.ApplyRoleBinding(rb, cluster, p.endpointFor(cluster))
}

// typedFromEvent extracts a typed object from an informer event
// payload. A DeletedFinalStateUnknown tombstone always means the
// object is gone, whatever the caller passed in, so the returned
// deleted flag is authoritative. ok is false for anything that cannot
// be resolved to T.
func typedFromEvent[T metav1.Object](obj any, deleted bool) (T, bool, bool) {
	if typed, ok := obj.(T); ok {
		return typed, deleted, true
	}
	tomb, ok := obj.(cache.DeletedFinalStateUnknown)
	if !ok {
		var zero T
		return zero, deleted, false
	}
	typed, ok := tomb.Obj.(T)
	if !ok {
		var zero T
		return zero, deleted, false
	}
	return typed, true, true
}

func clusterOf(obj metav1.Object) graph.LogicalCluster {
	name := logicalcluster.From(obj)
	if name == "" {
		return fallbackCluster
	}
	return graph.LogicalCluster(name.String())
}
