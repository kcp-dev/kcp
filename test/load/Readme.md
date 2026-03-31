# Load Testing

Load testing framework and loadtests for the kcp project.

## Architecture

Please refer to the [drawing of the general layout](./architecture.excalidraw).

## Setup

Installation scripts and manuals are provided in [setup/Readme](./setup/Readme.md).

## Usage

All test cases are organized in the `testing` folder. You can run the entire suite using:

```sh
go test ./testing/...
```

The tests will prompt you for any specific required variables and configs.

Alternatively you can run a subset of tests using standard `go test` syntax. E.g.:

```sh
go test ./testing/... -run ^TestExample
```

## Development

The load-testing framework itself is organized in the `pkg` folder. You can run its unit
tests directly using:

```sh
go test ./pkg/...
```

## Partitioning

You can partition your loadtest by providing it with a unique `start` number. Please be advised that this multiplexes your test. Any load you place will be multiplied by the number of partitions. Depending per test, adjust throughput values like qps accordingly.
