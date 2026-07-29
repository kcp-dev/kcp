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
	ctrllog.SetLogger(klog.NewKlogr())

	opts := server.NewOptions()
	fs := pflag.CommandLine
	klog.InitFlags(goflag.CommandLine)
	fs.AddGoFlagSet(goflag.CommandLine)
	opts.AddFlags(fs)
	pflag.Parse()

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
