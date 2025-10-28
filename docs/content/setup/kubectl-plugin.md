---
description: >
  How to install and use the kubectl kcp plugin.
---

# kubectl Plugins

kcp provides kubectl plugins that simplify the operations with the kcp server.

You can install them using [krew](https://krew.sigs.k8s.io/):

```sh
$ kubectl krew index add kcp-dev https://github.com/kcp-dev/krew-index.git
$ kubectl krew install kcp-dev/kcp
$ kubectl krew install kcp-dev/ws
$ kubectl krew install kcp-dev/create-workspace
```
!!! Note
    When installed directly from the git repository `kubectl create workspace` should be used instead of `kubectl create-workspace`.

The plugins will be [automatically discovered by your current `kubectl` binary](https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/):

```sh
$ kubectl kcp
kcp is the easiest way to manage Kubernetes applications against one or more clusters, by giving you a personal control plane that schedules your workloads onto one or many clusters, and making it simple to pick up and move. Advanced use cases including spreading your apps across clusters for resiliency, scheduling batch workloads onto clusters with free capacity, and enabling collaboration for individual teams without having access to the underlying clusters.

This command provides kcp-specific sub-command for kubectl.

Usage:
  kcp [command]

Available Commands:
  bind        Bind different types into current workspace.
  claims      Operations related to viewing or updating permission claims
  completion  Generate the autocompletion script for the specified shell
  crd         CRD related operations
  help        Help about any command
  workspace   Manages kcp workspaces

Flags:
      --add_dir_header                   If true, adds the file directory to the header of the log messages
      --alsologtostderr                  log to standard error as well as files (no effect when -logtostderr=true)
  -h, --help                             help for kcp
      --log_backtrace_at traceLocation   when logging hits line file:N, emit a stack trace (default :0)
      --log_dir string                   If non-empty, write log files in this directory (no effect when -logtostderr=true)
      --log_file string                  If non-empty, use this log file (no effect when -logtostderr=true)
      --log_file_max_size uint           Defines the maximum size a log file can grow to (no effect when -logtostderr=true). Unit is megabytes. If the value is 0, the maximum file size is unlimited. (default 1800)
      --logtostderr                      log to standard error instead of files (default true)
      --one_output                       If true, only write logs to their native severity level (vs also writing to each lower severity level; no effect when -logtostderr=true)
      --skip_headers                     If true, avoid header prefixes in the log messages
      --skip_log_headers                 If true, avoid headers when opening log files (no effect when -logtostderr=true)
      --stderrthreshold severity         logs at or above this threshold go to stderr when writing to files and stderr (no effect when -logtostderr=true or -alsologtostderr=true) (default 2)
  -v, --v Level                          number for the log level verbosity
      --version                          version for kcp
      --vmodule moduleSpec               comma-separated list of pattern=N settings for file-filtered logging

Use "kcp [command] --help" for more information about a command.

$ kubectl ws .                                # a short-cut for kubectl kcp workspace
$ kubectl create workspace my-workspace       # a short-cut for kubectl kcp workspace create
```
