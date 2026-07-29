/*
Copyright 2026 The kcp Authors.

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
