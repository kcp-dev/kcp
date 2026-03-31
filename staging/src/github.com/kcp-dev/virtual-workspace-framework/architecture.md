# Virtual Workspace Apiserver Architecture

The virtual workspace apiserver is based on k8s.io/apiserver and bundles multiple
delegated apiservers into a single apiserver. 

Usual k8s.io/apiserver delegation would mean a common filter chain and hence

  1. common authentication
  2. common authorization
  3. common OpenAPI endpoint
  4. all APIs under the same /apis/<group>.
  5. common discovery endpoints

In other words, an apiserver delegation chain serves one workspace only.

For virtual workspace apiserver in contrast, we need to serve multiple workspaces,
i.e.

  1. common authentication, as above, this is fine, but
  2. independent (custom) authorization
  3. independent OpenAPI endpoint <some-prefix>/openapi
  4. independent sets of APIs under <some-prefix>/apis/<group>
  5. independent discovery endpoints <some-prefix>/apis.

On the other hand, we want to keep using k8s.io/apiserver. To make this work with
the upper requirements, the handler chain will 

  a) inject the virtual workspace into the context
  b) rewrite the request path to remove the virtual workspace prefix.
  
The delegated apiserver will wrap the s.Handler's director (which usually decides
which requests to pass to gorestful) and check for the virtual workspace name in the
context. If it matches, the request is forwarded to the original director, and
otherwise the request is passed on to the delegate.

## TODOs / drawbacks:

- the authorizer needs a switch by virtual workspace, to implement custom authorization
- priority & fairnesss, if enabled, would not be by virtual workspace
