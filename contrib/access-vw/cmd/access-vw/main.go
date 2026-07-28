// Command access-vw runs the Access Virtual Workspace: a
// virtual-workspace root apiserver (kcp virtual-workspace-framework)
// serving the SelfClusterAccessReview API at /services/access, designed
// to sit behind kcp's front-proxy.
package main

import (
	goflag "flag"
	"os"

	"github.com/spf13/pflag"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/server"
)

func main() {
	// Wire controller-runtime's logger to klog so multicluster-runtime
	// and controller-runtime log through the standard pipeline.
	ctrllog.SetLogger(klog.NewKlogr())

	opts := server.NewOptions()

	fs := pflag.CommandLine
	klog.InitFlags(goflag.CommandLine)
	// controller-runtime's client/config package registers a
	// "kubeconfig" flag on the standard flag set in init(); adopt it
	// instead of redefining.
	fs.AddGoFlagSet(goflag.CommandLine)
	opts.AddFlags(fs)
	pflag.Parse()

	// If --kubeconfig came from controller-runtime's flag rather than
	// our own registration, copy the parsed value into the options.
	if opts.Kubeconfig == "" {
		if f := fs.Lookup("kubeconfig"); f != nil {
			opts.Kubeconfig = f.Value.String()
		}
	}

	ctx := genericapiserver.SetupSignalContext()

	if err := server.Run(ctx, opts); err != nil {
		klog.ErrorS(err, "access-vw failed")
		os.Exit(1)
	}
}
