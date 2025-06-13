# Continuous Integration (CI)

kcp uses a combination of [GitHub Actions](https://help.github.com/en/actions/automating-your-workflow-with-github-actions) and
and [prow](https://github.com/kubernetes/test-infra/tree/master/prow) to automate the build process.

Here are the most important links:

- [.github/workflows/](https://github.com/kcp-dev/kcp/blob/main/.github/workflows/) defines the Github Actions based jobs.
- [kcp-dev/kcp/.prow.yaml](https://github.com/kcp-dev/kcp/blob/main/.prow.yaml) defines the prow based jobs.

## Debugging Flakes

Tests that sometimes pass and sometimes fail are known as flakes. Sometimes, there is only an issue with the test, while
other times, there is an actual bug in the main code. Regardless of the root cause, it's important to try to eliminate
as many flakes as possible.

### Unit Test Flakes

If you're trying to debug a unit test flake, you can try to do something like this:

```shell
go test -race ./pkg/reconciler/apis/apibinding -run TestReconcileBinding -count 100 -failfast
```

This tests one specific package, running only a single test case by name, 100 times in a row. It fails as soon as it
encounters any failure. If this passes, it may still be possible there is a flake somewhere, so you may need to run
it a few times to be certain. If it fails, that's a great sign - you've been able to reproduce it locally. Now you
need to dig into the test condition that is failing. Work backwards from the condition and try to determine if the
condition is correct, and if it should be that way all the time. Look at the code under test and see if there are any
reasons things might not be deterministic.

### End to End Test Flakes

Debugging an end-to-end (e2e) test that is flaky can be a bit trickier than a unit test. Our e2e tests run in one of
two modes:

1. Tests share a single kcp server
2. Tests in a package share a single kcp server

The `e2e-shared-server` CI job uses mode 1, and the `e2e` CI job uses mode 2.

There are also a handful of tests that require a fully isolated kcp server, because they manipulate some configuration
aspects that are system-wide and would break all the other tests. These tests run in both `e2e` and `e2e-shared-server`,
separate from the other kcp instance(s).

You can use the same `run`, `-count`, and `-failfast` settings from the unit test section above for trying to reproduce
e2e flakes locally. Additionally, if you would like to operate in mode 1 (all tests share a single kcp server), you can
start a kcp instance locally in a separate terminal or tab:

```shell
bin/test-server
```

Then, to have your test use that shared kcp server, you add `-args --use-default-kcp-server` to your `go test` run:

```shell
go test ./test/e2e/apibinding -count 20 -failfast -args --use-default-kcp-server
```

