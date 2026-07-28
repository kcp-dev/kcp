package rbacprovider

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
)

func (p *Provider) runMulticluster(ctx context.Context, cfg *rest.Config, g *graph.Graph) error {
	if p.APIExportEndpointSlice == "" {
		return fmt.Errorf("APIExportEndpointSlice is required for multi-shard mode")
	}

	logger := log.FromContext(ctx).WithName("rbacprovider")

	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))
	utilruntime.Must(corev1alpha1.AddToScheme(sch))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha1.AddToScheme(sch))

	provider, err := apiexport.New(cfg, p.APIExportEndpointSlice, apiexport.Options{
		Scheme: sch,
		Log:    &logger,
	})
	if err != nil {
		return fmt.Errorf("construct apiexport provider: %w", err)
	}

	mgr, err := mcmanager.New(cfg, provider, manager.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"}, // disable; access-vw has its own HTTP server
	})
	if err != nil {
		return fmt.Errorf("construct multicluster manager: %w", err)
	}

	if err := registerRBACControllers(mgr, p.translator, p.endpointFor); err != nil {
		return fmt.Errorf("register controllers: %w", err)
	}

	if err := mgr.GetLocalManager().Add(manager.RunnableFunc(func(ctx context.Context) error {
		g.SetReady()
		logger.Info("access graph marked ready (multicluster manager started)")
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("register readiness runnable: %w", err)
	}

	return mgr.Start(ctx)
}

func registerRBACControllers(
	mgr mcmanager.Manager,
	t *Translator,
	endpointFor func(graph.LogicalCluster) string,
) error {
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("access-vw-clusterrolebinding").
		For(&rbacv1.ClusterRoleBinding{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			return reconcileCRB(ctx, mgr, t, endpointFor, req)
		})); err != nil {
		return fmt.Errorf("build CRB controller: %w", err)
	}

	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("access-vw-rolebinding").
		For(&rbacv1.RoleBinding{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			return reconcileRB(ctx, mgr, t, endpointFor, req)
		})); err != nil {
		return fmt.Errorf("build RB controller: %w", err)
	}

	return nil
}

func reconcileCRB(
	ctx context.Context,
	mgr mcmanager.Manager,
	t *Translator,
	endpointFor func(graph.LogicalCluster) string,
	req mcreconcile.Request,
) (ctrl.Result, error) {
	cluster := graph.LogicalCluster(req.ClusterName)

	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("get cluster %q: %w", req.ClusterName, err)
	}

	var crb rbacv1.ClusterRoleBinding
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &crb); err != nil {
		if apierrors.IsNotFound(err) {
			t.RemoveClusterRoleBinding(req.Name, cluster)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("get ClusterRoleBinding: %w", err)
	}
	t.ApplyClusterRoleBinding(&crb, cluster, endpointFor(cluster))
	return reconcile.Result{}, nil
}

func reconcileRB(
	ctx context.Context,
	mgr mcmanager.Manager,
	t *Translator,
	endpointFor func(graph.LogicalCluster) string,
	req mcreconcile.Request,
) (ctrl.Result, error) {
	cluster := graph.LogicalCluster(req.ClusterName)

	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("get cluster %q: %w", req.ClusterName, err)
	}

	var rb rbacv1.RoleBinding
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &rb); err != nil {
		if apierrors.IsNotFound(err) {
			t.RemoveRoleBinding(req.Namespace, req.Name, cluster)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("get RoleBinding: %w", err)
	}
	t.ApplyRoleBinding(&rb, cluster, endpointFor(cluster))
	return reconcile.Result{}, nil
}
