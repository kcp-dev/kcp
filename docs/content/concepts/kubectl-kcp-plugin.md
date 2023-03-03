---
description: >
  How to use the kubectl kcp plugin.
---

# kubectl kcp plugin

kcp provides a kubectl plugin that simplifies the operations with the kcp server.

You can install the plugin from the current repo:

```sh
$ make install
go install ./cmd/...
```

The plugin will be [automatically discovered by your current `kubectl` binary](https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/):

```sh
$ kubectl-kcp
KCP is the easiest way to manage Kubernetes applications against one or more clusters, by giving you a personal control plane that schedules your workloads onto one or many clusters, and making it simple to pick up and move. Advanced use cases including spreading your apps across clusters for resiliency, scheduling batch workloads onto clusters with free capacity, and enabling collaboration for individual teams without having access to the underlying clusters.

This command provides KCP specific sub-command for kubectl.

Usage:
  kcp [command]

Available Commands:
  completion  generate the autocompletion script for the specified shell
  help        Help about any command
  workspace   Manages KCP workspaces

Flags:
      --add_dir_header                   If true, adds the file directory to the header of the log messages
      --alsologtostderr                  log to standard error as well as files
  -h, --help                             help for kcp
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

Use "kcp [command] --help" for more information about a command.
```
