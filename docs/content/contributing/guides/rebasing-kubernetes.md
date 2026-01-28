# Rebasing Kubernetes

This guide describes the process of rebasing kcp onto a new Kubernetes version.

On a high level, what we need to do is: update our monorepo dependencies, update our kcp-dev/kubernetes fork, and update kcp-dev/kcp to make use of the new version of the fork. It is important to follow the order of the guide, so that all components are generated using the correct updated dependencies.

## 1. Before you start

Throughout this guide we are using the following variables. Please set them accordingly for the versions you are updating from and to:

```sh
export CUR_VERSION="1.33.3"
export NEW_VERSION="1.33.5"
export CUR_VERSION0="0.33.3"
export NEW_VERSION0="0.33.5"
```

## 2. Updating kcp-dev/kcp Monorepo

1. Create a new branch

    ```sh
    git switch -c "$NEW_VERSION-prep"
    ```

2. Update go.mod:
    1. Check the Go version inside kubernetes/kubernetes and update it if our go.mod is outdated
    2. Update the primary Kubernetes dependencies:

        ```sh
        go get k8s.io/api@v$NEW_VERSION0 k8s.io/apimachinery@v$NEW_VERSION0 k8s.io/client-go@v$NEW_VERSION0
        ```

    3. Run `go mod tidy && make modules`

### 2.1 Check apimachinery

Manually review the code that is upstream from `third_party` and make the same/similar edits to anything that changed upstream. For example, we maintain a slightly modified copy of the [shared informer code](https://github.com/kubernetes/kubernetes/blob/v1.30.1/staging/src/k8s.io/client-go/tools/cache/shared_informer.go).

### 2.2 Check client-go

Run `hack/populate-copies.sh`, this will copy files originally copied
from upstream over the local copies. Review the changes and ensure
changes from upstream are handled.

### 2.3 Check code-generator

Update any generators, if needed:

  1. Manually review upstream to check for any changes to these generators in the `kubernetes` repository:
      1. staging/src/k8s.io/code-generator/cmd/client-gen
      2. staging/src/k8s.io/code-generator/cmd/informer-gen
      3. staging/src/k8s.io/code-generator/cmd/lister-gen
  2. If there were any substantive changes, make the corresponding edits to our generators.
  3. You'll probably want to commit your changes at this point.

### 2.4 Verify everything is in order

Run `make clean build codegen lint test`

TODO @monorepo-specialists: Are these the correct commands? Or would you need to do you need to run `make clean build codegen clean build lint test` to ensure things are built after codegen has been run?

### 2.5 Get it merged

Create your PR, check that CI is passing and get it merged. It is paramount that you do not start with the next step in this guide until your PR is merged!

## 3. Rebasing kcp-dev/kubernetes

Please only start this part of the process, once you have your PR from step 2 merged successfully! Otherwise you will generate wrong modules and vendor files!

### 3.1 Overview and Conventions

#### Remotes

Inside `kcp-dev/kubernetes`, we will be using the following remotes. Please set them up if you are not already using them:

- `upstream` → kcp-dev/kubernetes
- `origin` → your fork of kcp-dev/kubernetes
- `kubernetes` → kubernetes/kubernetes

Make sure all of your remotes are up to date:

```sh
git fetch --all
```

#### Tags an Branches

Within our rebasing, we use the following tag and branch conventions:

- `v$CUR_VERSION` -> identifies a tag in kubernetes/kubernetes which points to an official Kubernetes release commit
- `kcp-$CUR_VERSION` -> identifies a branch in kcp-dev/kubernetes, which contains all upstream kubernetes/kubernetes commits plus our kcp specific changes

Since git does not differentiate tags based on remotes, it is important to not mess with these tag and branch conventions!

#### Commits

Commits merged into `kcp-dev/kubernetes` follow this commit message format:

- `UPSTREAM: <UPSTREAM PR ID>`
  - The number identifies a PR in upstream kubernetes
    (i.e. `https://github.com/kubernetes/kubernetes/pull/<pr id>`)
  - A commit with this message should only be picked into the subsequent rebase branch
    if the commits of the referenced PR are not included in the upstream branch.
  - To check if a given commit is included in the upstream branch, open the referenced
    upstream PR and check any of its commits for the release tag (e.g. `v.1.26.3`)
    targeted by the new rebase branch. For example: ![Example PR screenshot](../commit-tag.png)
- `UPSTREAM: <carry>:`
  - A persistent carry that should probably be picked for the subsequent rebase branch.
  - In general, these commits are used to modify behavior for consistency or
    compatibility with kcp.
- `UPSTREAM: <drop>:`
  - A carry that should probably not be picked for the subsequent rebase branch.
  - In general, these commits are used to maintain the codebase in ways that are
    branch-specific, like the update of generated files or dependencies.

If you are curious, a list of all commits we placed on top of kubernetes/kubernetes can be obtained using the following command:

```sh
git log --oneline --reverse --no-merges --no-decorate v$CUR_VERSION..upstream/kcp-$CUR_VERSION
```

## 3.2 Rebase Process

### Good to Know

- First and foremost, take notes of what worked/didn't work well. Update this guide based on your experiences!
- Remember, if you mess up, `git rebase --abort` and `git reflog` are your very good friends!
- We recommend to enable [Git Rerere](https://git-scm.com/book/en/v2/Git-Tools-Rerere) for this repository. It will allow you to record and reuse any merge conflict resolutions, allowing you to abort rebases at any time without loosing prior progress:

    ```sh
    mkdir .git/rr-cache
    ```

- During your rebase you will notice that kcp-dev/kubernetes is not compiling. This is expected, we will only see if everything is working correctly during the testing stage in the [integration step](#4-integrate-kcp-devkubernetes-into-kcp-devkcp)
- To make validating changes easier you can use these functions:

    ```bash
    # record the changed files between the version kcp will jump
    git diff --name-only v$CUR_VERSION v$NEW_VERSION > changed_files.txt

    # list_changed_files lists the files changed in the current commit
    list_changed_files() {
        for changed_file in $(git diff --name-only @ @~1); do
            if grep -q "$changed_file" changed_files.txt; then
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

### Branch Preparation

Ask a maintainer to create a new branch on kcp-dev/kubernetes called `kcp-$NEW_VERSION-baseline`, which should point to the commit the kubernetes/kubernetes `v$NEW_VERSION`-tag points to.

You can continue with the rest of this guide for now, you will need this branch towards the end of this section.

### Performing the Rebase

1. Prepare a list of all non-drop commits:

    ```sh
    git log --oneline --reverse --no-merges --no-decorate v$CUR_VERSION..upstream/kcp-$CUR_VERSION | grep -v '<drop>' > commits.txt
    ```

    You will be using this file to write comments on any commits which you have changed substantially or if you encountered conflicts. This is important for reviewers to understand the overall flow and changes of your rebase.

2. Create a new detached HEAD which rebases our changes onto the new upstream version. In the upcoming interactive dialogue, drop any commits which are marked with the `<drop>` keyword (we will re-create this later in this guide)

    ```sh
    # this takes all commits between the current version of our fork and kubernetes and places them on top of the new kubernetes tag
    git rebase --onto v$NEW_VERSION v$CUR_VERSION upstream/kcp-$CUR_VERSION --interactive
    ```

   - If you encounter conflicts, resolve them as best as you can.
   - If you have to make substantial changes, document it in the commit
    message, add your signed-off-by and add a note in your commits.txt.
   - Check the changes with `list_changed_files` and `view_changed_files`
      (see above).

    TODO @ntnn Let's discuss how/if we want to put in these two paragraphs

    ```txt
    If a commit applied clean and `list_changed_files` shows no output, you
    can move on to the next commit because there were no changes to the
    files that commit touched between the two kube versions.

    Additionally `view_changed_files` only shows the changes of the
    currently cherry-picked commit to files that were changed between the
    two kube versions.
    ```

3. Create a branch from our newly created, detached HEAD:

    ```sh
    git switch -c kcp-$NEW_VERSION
    ```

4. Check if any new controllers were added to Kubernetes. If so, determine if they are relevant to the control plane, or if they're specific to workloads (anything related to pods/nodes/etc.). If they're for the control plane and you think they should be enabled in kcp, you have 1 of 2 choices:

    1. Modify the controller in Kubernetes to be logical cluster aware; Check pkg/controller/namespace/namespace_controller.go for reference
    2. Add code to kcp to spawn a new controller instance scoped to a single logical cluster; Check garbage collector or quota controller for a reference

5. Check if any new admission plugins were added to Kubernetes. Decide if we need them in kcp. If so, make a note of them, and we'll add them to kcp below. TODO I cannot find in the guide where it is mentioned how to do that

6. Update kcp dependencies, for all kcp repositories that changed. For example:

    ```sh
    hack/pin-dependency.sh github.com/kcp-dev/logicalcluster/v3 <latest-release-tag>
    hack/pin-dependency.sh github.com/kcp-dev/apimachinery/v2 kcp-$NEW_VERSION
    hack/pin-dependency.sh github.com/kcp-dev/client-go kcp-$NEW_VERSION
    ```

7. Commit the dependency updates:

    ```sh
    git add .
    git commit -m 'CARRY: <drop>: Add KCP dependencies'
    ```

8. Update the vendor directory:

    ```sh
    hack/update-vendor.sh
    git add .
    git commit -m 'CARRY: <drop>: Update vendor'
    ```

    Warning: If update-vendor fails, do not run it again, as it is not an idempotent script! If you need to re-run it, you have to git reset first.

9. Update generated clients, informers, listers, etc. because we generate logical cluster aware versions of these for Kubernetes to use:

    ```sh
    hack/update-codegen.sh
    ```

    This can delete the `zz_generated.validations.go` - this is expected
    as we drop the validation-gen generator. See
    <https://github.com/kcp-dev/kcp/issues/3562a> for details.

    This step can pin kube dependencies to versions instead of v0.0.0,
    which can lead to opaque errors laters.
    To fix this run:

    ```sh
    find go.mod staging -iname go.mod \
    -exec sed -e "/k8s.io/ s#v$NEW_VERSION0#v0.0.0#" -i "{}" \;
    ```

    TODO @ntnn: I think the previous update vendor step already does this. But I was not sure if it is safe to run the sed before running update-codegen. Wdyt?

10. Commit the generated code changes:

    ```sh
    git add .
    git commit -m 'CARRY: <drop>: Update generated code'
    ```

11. Push your commits to your fork of Kubernetes in GitHub.

12. Open a pull request for review against the `kcp-$NEW_VERSION-baseline` branch and start its title with `WIP`so it gets created in draft mode - you don't want to merge anything just yet.

## 4. Integrate kcp-dev/kubernetes into kcp-dev/kcp

1. At this point, you're ready to try to integrate the updates into kcp proper. There is still likely a good amount of work to do, so don't get discouraged if you encounter dozens or hundreds of compilation issues at this point. That's totally normal!
2. Update go.mod:
    If any new staging repositories were added in between the old and new Kubernetes versions, you'll need to add
      those in the `replace` directive at the bottom of go.mod. For example, in the 1.31 rebase, we had to add 4:
      - k8s.io/dynamic-resource-allocation
      - k8s.io/kms
      - k8s.io/sample-cli-plugin
      - k8s.io/sample-controller

    Go ahead and make a commit here, as the next change we'll be making is to point kcp at your local checkout of Kubernetes.

3. Point kcp at your local checkout of Kubernetes:

    ```sh
    # Change KUBE per your local setup
    KUBE=../../../go/src/k8s.io/kubernetes
    gsed -i "s,k8s.io/\(.*\) => .*/kubernetes/.*,k8s.io/\1 => $KUBE/vendor/k8s.io/\1,;s,k8s.io/kubernetes => .*,k8s.io/kubernetes => $KUBE," go.mod
    ```

    !!! Warning: Don't commit your changes to go.mod/go.sum. They point to your local file system.
4. Resolve any conflicts
5. Run `make modules`
6. Run `make codegen`
7. Keep iterating to get all the code to compile
8. Get the `lint` and `test` make targets to pass
9. Get the `test-e2e` make targets to pass.

## 5. Test CI

1. Undo your changes to go.mod and go.sum that point to your local checkout:

    ```sh
    git checkout -- go.mod go.sum
    ```

2. Update go.mod to point to your rebase branch in your Kubernetes fork:

    ```sh
    GITHUB_USER=<your username here> GITHUB_REPO=<name of your fork> BRANCH=kcp-$NEW_VERSION-rebase hack/bump-k8s.sh
    ```

3. Commit this change with a message such as `UNDO: my Kubernetes`. We won't be using this commit when it's time to merge, so we make it easy to find.
4. Open a pull request in kcp. Wait for CI results. The `deps` job will always fail at this point because it's pointing to your fork of Kubernetes. This is expected, so don't worry. Your job at this point is to get all the other CI jobs to pass.

## 6. Get it Merged

1. Once CI is passing (except for the `deps` job, as expected), we're ready to merge!
2. Coordinate with a maintainer - show them the test results, then get them to approve your rebase PR in kcp-dev/kubernetes. Get the PR merged (this is something that currently must be done manually - ask someone with merge rights to do it for you if you need help).
3. Rename/create a new branch in kcp-dev/kubernetes based off what you just merged into kcp-$NEW_VERSION-baseline - this is the actual rebase! The new branch could be named something like `kcp-$NEW_VERSION`.
4. Back in your local checkout of kcp, update to the kcp-dev/kubernetes rebase branch:

    ```sh
    BRANCH=kcp-$NEW_VERSION hack/bump-k8s.sh
    ```

5. Commit this change. You can either squash this with your previous `UNDO` commit (reword it so it's not an `UNDO` any more), or drop the `UNDO` commit and replace it with this one.
6. Check on CI. Hopefully everything is green. If not, keep iterating on it.

## 7. Update the Default Branch in kcp-dev/kubernetes

Ask a maintainer to change the default branch in kcp-dev/kubernetes to `kcp-$NEW_VERSION$`
