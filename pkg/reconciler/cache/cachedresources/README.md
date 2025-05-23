# CachedResources controller

This controller acts on `CachedResources` objects and based on configured GroupVersionKind - replicated them into cache server.
To replicate objects 'wrapper' object is used `CachedObject`. Objects are named based on the original gvr. Example:

Object targeted by `CachedResource` bellow:
```yaml
apiVersion: machines.svm.io/v1alpha1
kind: Instance
metadata:
  name: web-server-1
```

would be replicated into cache server:
```yaml
apiVersion: cache.kcp.io/v1alpha1
Kind: CachedObject
metadata:
  name: v1alpha1.instances.machines.svm.io.web-server-1
  labels:
    cache.kcp.io/object-schema: v1alpha1.instances.machines.svm.io
    cache.kcp.io/object-group: machines.svm.io
    cache.kcp.io/object-version: v1alpha1
    cache.kcp.io/object-resource: instances
    cache.kcp.io/object-original-name: web-server-1
    cache.kcp.io/object-original-namespace: <empty>
```

Labels are used to filter object based on group, schema, etc. This way we can get individual object by knowing full gvr + name or just schema.

Main controller (`CachedResources`) for each `CachedResource` starts child replication controller with 2 informers:
* Local - targeting native type (in example `instances`) to get every change for it.
* Global - targeting `CachedObject` from the cache server

It will observe both cache and local states and makes sure object are replicated.
On deletion of `CachedResources` cache is purged and controller stopped after purge is done.


## Delete flow:

1. CachedResource is deleted
2. Root controller sets phase to `Deleting`
3. Root controller gives signal to child controller as its deleted so its stops replicating
4. Replication root controller will purge the selected resources and flips the status to Deleted
5. Once it flips status to Deleted - finalizer is removed and controller stopped.

# TODO

1. Make `global` informer filtered, so it filters only its own schema. Else, with multiple objects in the API it will get everything.
2. Add secrets reference for Identity hash
3. Potentially see if we can remove roundtripping to unstructured in the child replication controller.