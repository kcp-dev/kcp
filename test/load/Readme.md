# Load Testing

Load testing framework and loadtests for the kcp project. Please note this setup is intended to be
used for load-testing only and not setting up fully production grade environments.

## Architecture

Please refer to the [drawing of the general layout](./architecture.excalidraw).

## Setup

Installation scripts and manuals are provided in [setup/Readme](./setup/Readme.md).

## Usage

### Local Development

All test cases are organized in the `testing` folder. You can run the entire suite using:

```sh
go test ./testing/...
```

The tests will prompt you for any specific required variables and configs.

Alternatively you can run a subset of tests using standard `go test` syntax. E.g.:

```sh
go test ./testing/... -run ^TestExample
```

### Report Creation

If you are running the tests to create reports, please use the `suite.sh` helper script. It automatically
ensures that all files get generated using unified timestamps in a `.loadtest-results` folder.

```sh
./suite.sh TestExample
```

The script will show live output and is safe to run in jumphost environments where connectivity might
break.

## Development

The load-testing framework itself is organized in the `pkg` folder. You can run its unit
tests directly using:

```sh
go test ./pkg/...
```

## Partitioning

You can partition your loadtest by providing it with a unique `start` number. Please be advised that this multiplexes your test. Any load you place will be multiplied by the number of partitions. Depending per test, adjust throughput values like qps accordingly.
