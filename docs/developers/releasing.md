# Publishing a new kcp release
This document describes the processes to follow when creating and publishing a new kcp release.

Note, you currently need write access to the [kcp-dev/kcp](https://github.com/kcp-dev/kcp) repository to perform these tasks.

You also need an available
team member with approval permissions from https://github.com/openshift/release/blob/master/ci-operator/config/kcp-dev/kcp/OWNERS.

## Create git tags

### Prerequisite - make sure you have a GPG signing key

1. https://docs.github.com/en/authentication/managing-commit-signature-verification/generating-a-new-gpg-key
2. https://docs.github.com/en/authentication/managing-commit-signature-verification/adding-a-gpg-key-to-your-github-account
3. https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key

### Create the tags

kcp has 2 go modules, and a unique tag is needed for each module every time we create a new release.

1. `git fetch` from the main kcp repository (kcp-dev/kcp) to ensure you have the latest commits
2. Tag the main module
   1. If your git remote for kcp-dev/kcp is named something other than `upstream`, change `REF` accordingly
   2. If you are creating a release from a release branch, change `main` in `REF` accordingly, or you can
      make `REF` a commit hash.

    ```shell
    REF=upstream/main
    TAG=v1.2.3
    git tag --sign --message "$TAG" "$TAG" "$REF" 
    ```

3. Tag the `pkg/apis` module, following the same logic as above for `REF` and `TAG`

    ```shell
    REF=upstream/main
    TAG=v1.2.3
    git tag --sign --message "pkg/apis/$TAG" "pkg/apis/$TAG" "$REF" 
    ```

### Push the tags

```shell
REMOTE=upstream
TAG=v1.2.3
git push "$REMOTE" "$TAG" "pkg/apis/$TAG"
```

## If it's a new minor version

If this is the first release of a new minor version (e.g. the last release was v0.7.x, and you are releasing the first
0.8.x version), follow the following steps.

Otherwise, you can skip to [Review/edit/publish the release in GitHub](#revieweditpublish-the-release-in-github)

### Create a release branch

Set `REMOTE`, `REF`, and `VERSION` as appropriate.

```shell
REMOTE=upstream
REF="$REMOTE/main"
VERSION=1.2
git checkout -b "release-$VERSION" "$REF"
git push "$REMOTE" "release-$VERSION"
```

### Configure prow for the new release branch

1. Make sure you have [openshift/release](https://github.com/openshift/release/) cloned
2. Create a new branch
3. Copy `ci-operator/config/kcp-dev/kcp/kcp-dev-kcp-main.yaml` to `ci-operator/config/kcp-dev/kcp/kcp-dev-kcp-release-<version>.yaml`
4. Edit the new file
   1. Change `main` to the name of the release branch, such as `release-0.8`

       ```yaml
       zz_generated_metadata:
         branch: main
       ```

   2. Change `latest` to the name of the release branch

       ```yaml
       promotion:
         namespace: kcp
         tag: latest
         tag_by_commit: true
       ```

5. Edit `core-services/prow/02_config/kcp-dev/kcp/_prowconfig.yaml`
   1. Copy the `main` branch configuration to a new `release-x.y` entry
6. Run `make update`
7. Add the new/updated files and commit your changes
8. Push your branch to your fork
9. Open a pull request
10. Wait for it to be reviewed and merged

### Update testgrid

1. Make sure you have a clone of [kubernetes/test-infra](https://github.com/kubernetes/test-infra/)
2. Edit config/testgrids/kcp/kcp.yaml
   1. In the `test_groups` section:
      1. Copy all the entries under `# main` to the bottom of the map
      2. Rename `-main-` to `-release-<version>-`
   2. In the `dashboard_groups` section:
      1. Add a new entry under `dashboard_names` for `kcp-release-<version>`
   3. In the `dashboards` section:
      1. Copy the `kcp-main` entry, including `dashboard_tab` and all its entries, to a new entry called `kcp-release-<version>`
      2. Rename `main` to `release-<version>` in the new entry
3. Commit your changes
4. Push your branch to your fork
5. Open a pull request
6. Wait for it to be reviewed and merged

## Review/edit/publish the release in GitHub

The [goreleaser](https://github.com/kcp-dev/kcp/actions/workflows/goreleaser.yml) workflow automatically creates a draft GitHub release for each tag.

1. Navigate to the draft release for the tag you just pushed. You'll be able to find it under the [releases](https://github.com/kcp-dev/kcp/releases) page.
2. If the release notes have been pre-populated, delete them.
3. For the "previous tag," select the most recent, appropriate tag as the starting point
   1. If this is a new minor release (e.g. v0.8.0), select the initial version of the previous minor release (e.g. v0.7.0)
   2. If this is a patch release (e.g. v0.8.7), select the previous patch release (e.g. v0.8.6)
4. Click "Generate release notes"
5. Publish the release

## Notify

1. Create an email addressed to kcp-dev@googlegroups.com and kcp-users@googlegroups.com
   1. Subject: `[release] <version>` e.g. `[release] v0.8.0`
   2. In the body, include noteworthy changes
   3. Provide a link to the release in GitHub for the full release notes
2. Post a message in the [#kcp-dev](https://kubernetes.slack.com/archives/C021U8WSAFK) Slack channel
