# Rebasing Kubernetes

This describes the process of rebasing kcp onto a new Kubernetes version. For the examples below, we'll be rebasing
onto v1.33.3

# 1. Update kcp-dev/apimachinery

1. Create a new branch for the update, such as `1.31-prep`.
2. Update go.mod:
   1. You may need to change the go version at the top of the file to match what's in go.mod in the root of the
      Kubernetes repo. This is not required, but probably a good idea.
   2. Update the primary Kubernetes dependencies:
      ```
      go get k8s.io/api@v0.31.0  k8s.io/apimachinery@v0.31.0 k8s.io/client-go@v0.31.0
      ```
   3. Run `go mod tidy`.
3. Manually review the code that is upstream from `third_party` and make the same/similar edits to anything that
   changed upstream. For example, we maintain a slightly modified copy of the
   [shared informer code](https://github.com/kubernetes/kubernetes/blob/v1.30.1/staging/src/k8s.io/client-go/tools/cache/shared_informer.go).
4. Run `make lint test`. Fix any issues you encounter.
5. Commit your changes.
6. Push to your fork.
7. Open a PR; get it reviewed and merged.

# 2. Update kcp-dev/code-generator

1. Create a new branch for the update, such as `1.26-prep`.
2. Update `go.mod`:
   1. You may need to change the go version at the top of the file to match what's in go.mod in the root of the
      Kubernetes repo. This is not required, but probably a good idea.
   2. Update the primary Kubernetes dependencies:
      ```
      go get k8s.io/apimachinery@v0.31.0 k8s.io/code-generator@v0.31.0
      ```
   3. Run `go mod tidy`.
3. Repeat step 2 for `examples/go.mod`:
4. Update generators, if needed:
   1. Manually review upstream to check for any changes to these generators in the `kubernetes` repository:
      1. staging/src/k8s.io/code-generator/cmd/client-gen
      2. staging/src/k8s.io/code-generator/cmd/informer-gen
      3. staging/src/k8s.io/code-generator/cmd/lister-gen
   2. If there were any substantive changes, make the corresponding edits to our generators.
   3. You'll probably want to commit your changes at this point.
5. Run `make codegen`. You'll probably want to commit these changes as a standalone commit.
6. Run `make lint test build` and fix any issues you encounter.
7. Commit any remaining changes.
8. Push to your fork.
9. Open a PR; get it reviewed and merged.

# 3. Update kcp-dev/client-go
1. Create a new branch for the update, such as `1.26-prep`.
2. Update go.mod:
   1. You may need to change the go version at the top of the file to match what's in go.mod in the root of the
      Kubernetes repo. This is not required, but probably a good idea.
   2. Update the `kcp-dev/apimachinery` dependency:
      ```
      go get github.com/kcp-dev/apimachinery@main github.com/kcp-dev/code-generator@main
      ```
   3. That should have updated the primary Kubernetes dependencies, but in case it didn't, you can do so manually:
      ```
      go get k8s.io/api@v0.31.0 k8s.io/apimachinery@v0.31.0 k8s.io/client-go@v0.31.0 k8s.io/apiextensions-apiserver@v0.31.0
      ```
   4. Run `go mod tidy`.
3. Run `hack/populate-copies.sh`, this will copy files originally copied
   from upstream over the local copies. Review the changes and ensure
   changes from upstream are handled.
4. Run `make codegen`. You'll probably want to commit these changes as a standalone commit.
5. Run `make lint` and fix any issues you encounter.
6. Commit any remaining changes.
7. Push to your fork.
8. Open a PR; get it reviewed and merged.

# 4. Update kcp-dev/kubernetes

## Terminology

Commits merged into `kcp-dev/kubernetes` follow this commit message format:

- `UPSTREAM: <UPSTREAM PR ID>`
  - The number identifies a PR in upstream kubernetes
    (i.e. `https://github.com/kubernetes/kubernetes/pull/<pr id>`)
  - A commit with this message should only be picked into the subsequent rebase branch
    if the commits of the referenced PR are not included in the upstream branch.
  - To check if a given commit is included in the upstream branch, open the referenced
    upstream PR and check any of its commits for the release tag (e.g. `v.1.26.3`)
    targeted by the new rebase branch. For example:
    <img src="commit-tag.png">
- `UPSTREAM: <carry>:`
  - A persistent carry that should probably be picked for the subsequent rebase branch.
  - In general, these commits are used to modify behavior for consistency or
    compatibility with kcp.
- `UPSTREAM: <drop>:`
  - A carry that should probably not be picked for the subsequent rebase branch.
  - In general, these commits are used to maintain the codebase in ways that are
    branch-specific, like the update of generated files or dependencies.

## Rebase Process

1. First and foremost, take notes of what worked/didn't work well. Update this guide based on your experiences!
2. Remember, if you mess up, `git rebase --abort` and `git reflog` are your very good friends!
3. Terminology:
   - "Old" version: the current version of Kubernetes that kcp is using
   - "New" version: the new Kubernetes version on top of which you are rebasing
4. The `upstream` in e.g. `upstream/kcp-feature-logical-cluster-1.24-v3` is the name of the git remote that points
   to github.com/kcp-dev/kubernetes. Please adjust the commands below if your remote is named differently.
5. Prepare a list of commits:
    1. Checkout the current fork branch:
       ```
       git checkout kcp-dev/kcp-1.32.3
       ```
    2. Export the commits to cherry-pick them:
       ```
       git log --oneline --reverse --no-merges --no-decorate v1.32.3..HEAD \
           | grep -v '<drop>' > commits.txt
       ```
       You will use this commits.txt to also note conflicts and changes
       you had to make, so reviewers know where to focus their
       attention.
6. Prepare a branch to cherry-pick the commits onto:
   ```
   git reset --hard v1.33.3
   ```

Note: To make validating changes easier you can use these functions:

```bash
# record the changed files between the version kcp will jump
git diff --name-only v1.32.3 v1.33.3 > ../changed_files.txt

# list_changed_files lists the files changed in the current commit
list_changed_files() {
    for changed_file in $(git diff --name-only @ @~1); do
        if grep -q "$changed_file" ../changed_files.txt; then
            echo "./$changed_file"
        fi
    done
}

# view_changed_files shows the diff of the files changed in the current
# commit
view_changed_files() {
    local changed="$(list_changed_files)"
    [[ -z "$changed" ]] && return 0
    git diff-tree -r -p @  -- $changed
}
```

If a commit applied clean and `list_changed_files` shows no output, you
can move on to the next commit because there were no changes to the
files that commit touched between the two kube versions.

Additionally `view_changed_files` only shows the changes of the
currently cherry-picked commit to files that were changed between the
two kube versions.

7. Cherry-pick each commit one by one.
   1. If you encounter conflicts resolve them as best as you can.
   2. If you have to make substantial changes document it in the commit
      message, add your signed-off-by and add a note in your commits.txt.
   3. Check the changes with `list_changed_files` and `view_changed_files`
      (see above).

8. Check if any new controllers were added to Kubernetes. If so, determine if they are relevant to the control
    plane, or if they're specific to workloads (anything related to pods/nodes/etc.). If they're for the control
    plane and you think they should be enabled in kcp, you have 1 of 2 choices:

    1. Modify the controller in Kubernetes to be logical cluster aware
    2. Add code to kcp to spawn a new controller instance scoped to a single logical cluster

    For option 1, follow what we did in pkg/controller/namespace/namespace_controller.go.

    For option 2, follow what we did in kcp in either the garbage collector or quota controllers.

11. Check if any new admission plugins were added to Kubernetes. Decide if we need them in kcp. If so, make a note
    of them, and we'll add them to kcp below.


12. Update kcp dependencies, for all kcp repositories that changed. For example:
    ```
    hack/pin-dependency.sh github.com/kcp-dev/logicalcluster/v3 v3.0.4
    hack/pin-dependency.sh github.com/kcp-dev/apimachinery/v2 kcp-1.33.3
    hack/pin-dependency.sh github.com/kcp-dev/client-go kcp-1.33.3
    ```

13. Commit the dependency updates:
    ```
    git add .
    git commit -m 'CARRY: <drop>: Add KCP dependencies'
    ```

14. Update the vendor directory:
   ```
   hack/update-vendor.sh
   git add .
   git commit -m 'CARRY: <drop>: Update vendor'
   ```

15. Update generated clients, informers, listers, etc. because we generate logical cluster aware versions of these
   for Kubernetes to use:
   ```
   hack/update-codegen.sh
   ```

   This can delete the `zz_generated.validations.go` - this is expected
   as we drop the validation-gen generator. See
   https://github.com/kcp-dev/kcp/issues/3562a for details.

   This step can pin kube dependencies to versions instead of v0.0.0,
   which can lead to opaque errors laters.
   To fix this run:
   ```
   gsed -e '/k8s.io/ s#v0.33.3#v0.0.0#' -i "go.mod"
   find staging -iname go.mod \
       | while read go_mod; do
           gsed -e '/k8s.io/ s#v0.33.3#v0.0.0#' -i "$go_mod"
       done
   ```

16. Commit the generated code changes:
    ```
    git add .
    git commit -m 'CARRY: <drop>: Update generated code'
    ```

17. Push your commits to your fork of Kubernetes in GitHub.

18. Open a pull request for review **against the baseline branch, e.g. kcp-1.26-baseline**, but mark it `WIP` and maybe
    even open it in draft mode - you don't want to merge anything just yet.

# 5. Update kcp-dev/kcp

1. At this point, you're ready to try to integrate the updates into kcp proper. There is still likely a good
   amount of work to do, so don't get discouraged if you encounter dozens or hundreds of compilation issues at
   this point. That's totally normal!
2. Update go.mod:
   1. You'll need to adjust the versions of kcp-dev/apimachinery, client-go, and logicalcluster to match any changes
      you made in previous steps. Make sure the versions that are in the Kubernetes go.mod match the versions used here.
   2. If any new staging repositories were added in between the old and new Kubernetes versions, you'll need to add
      those in the `replace` directive at the bottom of go.mod. For example, in the 1.31 rebase, we had to add 4:
      - k8s.io/dynamic-resource-allocation
      - k8s.io/kms
      - k8s.io/sample-cli-plugin
      - k8s.io/sample-controller
   3. Go ahead and make a commit here, as the next change we'll be making is to point kcp at your local checkout of
      Kubernetes.
3. Point kcp at your local checkout of Kubernetes:
   ```
   # Change KUBE per your local setup
   KUBE=../../../go/src/k8s.io/kubernetes
   gsed -i "s,k8s.io/\(.*\) => .*/kubernetes/.*,k8s.io/\1 => $KUBE/vendor/k8s.io/\1,;s,k8s.io/kubernetes => .*,k8s.io/kubernetes => $KUBE," go.mod
   ```
   !!! warning
      Don't commit your changes to go.mod/go.sum. They point to your local file system.
4. Resolve any conflicts
5. Run `make modules`
6. Run `make codegen`
7. Keep iterating to get all the code to compile
8. Get the `lint` and `test` make targets to pass
9. Get the `e2e-*` make targets to pass.

# 6. Test CI

1. Undo your changes to go.mod and go.sum that point to your local checkout:
   ```
   git checkout -- go.mod go.sum
   ```
2. Update go.mod to point to your rebase branch in your Kubernetes fork:
   ```
   GITHUB_USER=<your username here> BRANCH=kcp-1.31-rebase hack/bump-k8s.sh
   ```
3. Commit this change with a message such as `UNDO: my Kubernetes`. We won't be using this commit when it's time to
   merge, so we make it easy to find.
4. Open a pull request in kcp. Wait for CI results. The `deps` job will always fail at this point because it's
   pointing to your fork of Kubernetes. This is expected, so don't worry. Your job at this point is to get all the
   other CI jobs to pass.

# 7. Get it Merged!

1. Once CI is passing (except for the `deps` job, as expected), we're ready to merge!
2. Coordinate with another project member - show them the test results, then get them to approve your rebase PR in
   kcp-dev/kubernetes. Get the PR merged (this is something that currently must be done manually - ask someone with
   merge rights to do it for you if you need help).
3. Rename/create a new branch in kcp-dev/kubernetes based off what you just merged into kcp-1.31-baseline - this is
   the actual rebase! The new branch could be named something like `kcp-1.31`.
4. Back in your local checkout of kcp, update to the kcp-dev/kubernetes rebase branch:
   ```
   BRANCH=kcp-1.31 hack/bump-k8s.sh
   ```
5. Commit this change. You can either squash this with your previous `UNDO` commit (reword it so it's not an `UNDO`
   any more), or drop the `UNDO` commit and replace it with this one.
6. Check on CI. Hopefully everything is green. If not, keep iterating on it.

# 7. Update the Default Branch in kcp-dev/kubernetes

1. Change it to your new rebase branch, e.g. `kcp-1.31`
