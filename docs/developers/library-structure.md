# Library Structure

The kcp library is set up and initialized in several phases. This document describes those phases and the responsibilities of each.

```go
opts := options.NewOptions()
completedOptions := opts.Complete()
conf := config.NewConfig(completedOptions)
completedConfig := conf.Complete()
srv := server.NewServer(completedConfig)
srv.Init()
srv.Run(ctx)
```

## Options

Options parses basic configuration options, such as flags or environment variables. `NewOptions()` does default parsing
of environment variables and CLI flags and sets defaults. You then can change options properties
as you see fit.

With `Options` in hand, you need to run `opts.Complete()`. This ensures that any modifications necessary,
such as defaults, are in place.

## Config

Config includes all of the structures required for the server to operate properly. It can include handlers,
callbacks, and other structures. These settings are informed by the values of `Options`, and so
take `Options` as a parameter.

With `Config` in hand, you need to run `conf.Complete()`. This ensures that any modifications necessary,
such as defaults, are in place.

## Server

Server is the actual structure that handles the inbound requests, passing them through RBAC, storing data, invoking handlers, etc.
It is, in effect, the API server.

To create a Server, you must call `NewServer()`, passing it the `CompletedOptions`. Then you initialize it with `Init()`, and finally
you `Run()` it.