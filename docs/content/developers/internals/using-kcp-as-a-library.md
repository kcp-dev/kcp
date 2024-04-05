---
linkTitle: "Library Usage"
description: >
  How to use kcp as a library.
---

# Using kcp as a library

Instead of running the kcp as a binary using `go run`, you can include the kcp api-server in your own projects. To create and start the api-server with the default options (including an embedded etcd server):

```go
options, err := serveroptions.NewOptions().Complete()
if err != nil {
    panic(err)
}

cfg, err := server.NewConfig(options)
if err != nil {
    panic(err)
}

completedCfg, err := cfg.Complete()
if err != nil {
    panic(err)
}

srv, err := server.NewServer(completedCfg)
if err != nil {
    panic(err)
}

// Run() will block until the apiserver stops or an error occurs.
if err := srv.Run(ctx); err != nil {
    panic(err)
}
```

You may also configure post-start hooks which are useful if you need to start a some process that depends on a connection to the newly created api-server such as a controller manager.

```go
// Create a new api-server with default options
options, err := serveroptions.NewOptions().Complete()
if err != nil {
    panic(err)
}

cfg, err := server.NewConfig(options)
if err != nil {
    panic(err)
}

completedCfg, err := cfg.Complete()
if err != nil {
    panic(err)
}

srv, err := server.NewServer(completedCfg)
if err != nil {
    panic(err)
}

// Register a post-start hook that connects to the api-server
srv.AddPostStartHook("connect-to-api", func(ctx genericapiserver.PostStartHookContext) error {
    // Create a new client using the client config from our newly created api-server
    client := clientset.NewForConfigOrDie(ctx.LoopbackClientConfig)
    _, err := client.Discovery().ServerGroups()

    return err
})

// Start the api-server
if err := srv.Run(ctx); err != nil {
    panic(err)
}
```
