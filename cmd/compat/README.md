# `compat`

This tool can be used to check compatibility between two CRD definitions, and
generate the Least-Common-Denominator (LCD) CRD YAML if requested.

## Usage

### Check compatibility of two files

```
go run ./cmd/compat old-crd.yaml new-crd.yaml
```

If the `--lcd` flag is passed, `compat` will print the LCD CRD YAML to stdout.

### Check compatibility between a CRD in a cluster and configuration in a local file

```
go run ./cmd/compat <(kubectl get crd foos.foo.example.dev -oyaml) foo-crd.yaml
```
