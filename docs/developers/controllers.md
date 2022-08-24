# Writing kcp-aware controllers

## Keys for objects in listers/indexers

When you need to get an object from a kcp-aware lister or an indexer, you can't just pass the object's name to the
`Get()` function, like you do with a typical controller targeting Kubernetes. Projects using kcp's copy of client-go
are using a modified key function.

Here are what keys look like for an object `foo` for both cluster-scoped and namespace-scoped varieties:

|Organization|Workspace|Logical Cluster|Namespace|Key|
|-|-|-|-|-|
|-|-|root|-|root|foo|
|-|-|root|default|default/root|foo|
|root|my-org|root:my-org|-|root:my-org|foo|
|root|my-org|root:my-org|default|default/root:my-org|foo|
|my-org|my-workspace|my-org:my-workspace|-|my-org:my-workspace|foo|
|my-org|my-workspace|my-org:my-workspace|default|default/my-org:my-workspace|foo|

## Encoding/decoding keys 

The `apimachinerycache "github.com/kcp-dev/apimachinery/pkg/cache"` package provides cluster-aware encoding and decoding functions.



### Encoding workspace keys
To encode a key **for a workspace**, use `helper.WorkspaceKey(org, ws)`. Valid values for `org` are `root` and any
organization workspace name (e.g. `my-org` from above).

### Encoding all other keys
To encode a key for anything else, use `clusters.ToClusterAwareKey(clusterName, name)`. If your object is namespace-scoped,
you'll need to do `ns + "/" + clusters.ToClusterAwareKey(clusterName, name)`.

### Decoding keys
To decode a key, use `apimachinerycache.SplitClusterAwareKey(key)`:

```go
clusterName, namespace, name, err := apimachinerycache.SplitClusterAwareKey(key)
if err != nil {
	// handle error
}
```