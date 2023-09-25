---
description: >
  Information on the kcp release process.
---

# Publishing a new kcp release

!!! note
    You currently need write access to the [kcp-dev/kcp](https://github.com/kcp-dev/kcp) repository to perform these
    tasks.

    You also need an available team member with approval permissions from <https://github.com/openshift/release/blob/master/ci-operator/config/kcp-dev/kcp/OWNERS>.

## Create git tags

### Prerequisite - make sure you have a GPG signing key

1. <https://docs.github.com/en/authentication/managing-commit-signature-verification/generating-a-new-gpg-key>
2. <https://docs.github.com/en/authentication/managing-commit-signature-verification/adding-a-gpg-key-to-your-github-account>
3. <https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key>

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

 3. Tag the `sdk` module, following the same logic as above for `REF` and `TAG`

    ```shell
    REF=upstream/main
    TAG=v1.2.3
    git tag --sign --message "sdk/$TAG" "sdk/$TAG" "$REF"
    ```
### Push the tags

```shell
REMOTE=upstream
TAG=v1.2.3
git push "$REMOTE" "$TAG" "sdk/$TAG"
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
