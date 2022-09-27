## kcp workload sync

Create a synctarget in kcp with service account and RBAC permissions. Output a manifest to deploy a syncer for the given sync target in a physical cluster.

```
kcp workload sync <sync-target-name> --syncer-image <kcp-syncer-image> [--resources=<resource1>,<resource2>..] -o <output-file> [flags]
```

### Examples

```

	# Ensure a syncer is running on the specified sync target.
	kubectl kcp workload sync <sync-target-name> --syncer-image <kcp-syncer-image> -o syncer.yaml
	KUBECONFIG=<pcluster-config> kubectl apply -f syncer.yaml

	# Directly apply the manifest
	kubectl kcp workload sync <sync-target-name> --syncer-image <kcp-syncer-image> -o - | KUBECONFIG=<pcluster-config> kubectl apply -f -

```

### Options

```
      --as-uid string                  UID to impersonate for the operation
      --burst int                      Burst to use when talking to API servers. (default 30)
      --certificate-authority string   Path to a cert file for the certificate authority
      --context string                 The name of the kubeconfig context to use
      --feature-gates string           A set of key=value pairs that describe feature gates for alpha/experimental features. Options are:
                                       ContextualLogging
                                       KCPLocationAPI
                                       AdvancedAuditing
                                       APIResponseCompression
                                       APIListChunking
                                       APIPriorityAndFairness
                                       CustomResourceValidationExpressions
                                       KCPSyncerTunnel
                                       DryRun
                                       ServerSideApply
  -h, --help                           help for sync
      --insecure-skip-tls-verify       If true, the server's certificate will not be checked for validity. This will make your HTTPS connections insecure
      --kcp-namespace string           The name of the kcp namespace to create a service account in. (default "default")
      --kubeconfig string              path to the kubeconfig file
  -n, --namespace string               The namespace to create the syncer in in the physical cluster. By default this is "kcp-syncer-<synctarget-name>-<uid>".
  -o, --output-file string             The manifest file to be created and applied to the physical cluster. Use - for stdout.
      --password string                Password for basic authentication to the API server
      --proxy-url string               If provided, this URL will be used to connect via proxy
      --qps float32                    QPS to use when talking to API servers. (default 20)
      --replicas int                   Number of replicas of the syncer deployment. (default 1)
      --resources strings              Resources to synchronize with kcp.
      --server string                  The address and port of the Kubernetes API server
      --syncer-image string            The syncer image to use in the syncer's deployment YAML. Images are published at https://github.com/kcp-dev/kcp/pkgs/container/kcp%2Fsyncer.
      --tls-server-name string         If provided, this name will be used to validate server certificate. If this is not provided, hostname used to contact the server is used.
      --token string                   Bearer token for authentication to the API server
      --user string                    The name of the kubeconfig user to use
      --username string                Username for basic authentication to the API server
```

### Options inherited from parent commands

```
      --add_dir_header                   If true, adds the file directory to the header of the log messages
      --alsologtostderr                  log to standard error as well as files
      --log_backtrace_at traceLocation   when logging hits line file:N, emit a stack trace (default :0)
      --log_dir string                   If non-empty, write log files in this directory
      --log_file string                  If non-empty, use this log file
      --log_file_max_size uint           Defines the maximum size a log file can grow to. Unit is megabytes. If the value is 0, the maximum file size is unlimited. (default 1800)
      --logtostderr                      log to standard error instead of files (default true)
      --one_output                       If true, only write logs to their native severity level (vs also writing to each lower severity level)
      --skip_headers                     If true, avoid header prefixes in the log messages
      --skip_log_headers                 If true, avoid headers when opening log files
      --stderrthreshold severity         logs at or above this threshold go to stderr (default 2)
  -v, --v Level                          number for the log level verbosity
      --vmodule moduleSpec               comma-separated list of pattern=N settings for file-filtered logging
```

### SEE ALSO

* [kcp workload](kcp_workload.md)	 - Manages KCP sync targets

###### Auto generated by spf13/cobra on 23-Sep-2022
