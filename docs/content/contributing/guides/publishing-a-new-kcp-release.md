---
description: >
  Information on the kcp release process.
---

# Publishing a New kcp Release

!!! note
    You currently need write access to the [kcp-dev/kcp](https://github.com/kcp-dev/kcp) repository to perform these
    tasks.

## Create git Tags

### Prerequisite - Make Sure You Have a GPG Signing Key

1. <https://docs.github.com/en/authentication/managing-commit-signature-verification/generating-a-new-gpg-key>
2. <https://docs.github.com/en/authentication/managing-commit-signature-verification/adding-a-gpg-key-to-your-github-account>
3. <https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key>

### Create the Tag

!!! note
    kcp used two have two submodules (`sdk` and `cli`) which were tagged separately in addition to the main kcp module.
    With the introduction of the monorepo, those two submodules became staging repositories. Their tags are now handled
    automatically by publishing-bot, so it's only needed to create one tag for the main kcp module.

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

### Push the Tag

```shell
REMOTE=upstream
TAG=v1.2.3
git push "$REMOTE" "$TAG"
```

## If it's a New Minor Version

If this is the first release of a new minor version (e.g. the last release was v0.7.x, and you are releasing the first
0.8.x version), follow the following steps.

Otherwise, you can skip to [Generate release notes](#generate-release-notes)

### Create a Release Branch

Set `REMOTE`, `REF`, and `VERSION` as appropriate.

```shell
REMOTE=upstream
REF="$REMOTE/main"
VERSION=1.2
git checkout -b "release-$VERSION" "$REF"
git push "$REMOTE" "release-$VERSION"
```

### Update the `publishing-bot` Rules

The [`publishing-bot` rules](https://github.com/kcp-dev/kcp/blob/main/staging/publishing/rules.yaml) must be updated
to add a new branch for all staging repositories.

While this can be done manually, the easiest and recommended way is to use the `update-rules` tool.
This tool can be installed by cloning kcp's fork of [publishing-bot](https://github.com/kcp-dev/publishing-bot)
and running `make build` in the repository, which will install the tool in `_output/update-rules`.
Alternatively, the tool can be ran inside a Docker container using the `ghcr.io/kcp-dev/publishing-bot:latest` image.

The tool takes the path to the rules file, the branch name, and the Go version used on that branch:

```shell
_output/update-rules -branch release-0.x -go 1.x.y -rules ../kcp/staging/publishing/rules.yaml -o /tmp/rules.yaml
```

This will generate a temporary rules file in `/tmp/rules.yaml`. You'll need to replace `staging/publishing/rules.yaml`
in the kcp repo with that temporary file and push it to the repo before proceeding. The new rules file will be used
on the next publishing-bot run automatically.

## Generate Release Notes

To generate release notes from the information in PR descriptions you should use Kubernetes' [release-notes](https://github.com/kubernetes/release/tree/master/cmd/release-notes) tool.
This tool will use the `release-notes` blocks in PR descriptions and the `kind/` labels on those PRs to find user-facing changes and categorize them.
You can run the command below to install the latest version of it:

```shell
go install k8s.io/release/cmd/release-notes@latest
```

To use `release-notes` you will need to generate a GitHub API token (Settings -> Developers settings -> Personal access tokens -> Fine-grained tokens). A token with _Public Repositories (read-only)_ repository access and no further permissions is sufficient. Store the token somewhere safe and export it as `GITHUB_TOKEN` environment variable.

Then, run run the `release-notes` tool (set `PREV_VERSION` to the version released before the one you have just released).

```shell
TAG=v1.2.3
PREV_TAG=v1.2.2
release-notes \
  --required-author='' \
  --org kcp-dev \
  --repo kcp \
  --branch main \
  --start-rev $PREV_TAG \
  --end-rev $TAG \
  --output CHANGELOG.md 
```

Don't commit the `CHANGELOG.md` to the repository, just keep it around to update the release on GitHub (next step).

## Review/Edit/Publish the Release in GitHub

The [goreleaser](https://github.com/kcp-dev/kcp/actions/workflows/goreleaser.yml) workflow automatically creates a draft GitHub release for each tag.

1. Navigate to the draft release for the tag you just pushed. You'll be able to find it under the [releases](https://github.com/kcp-dev/kcp/releases) page.
2. If the release notes have been pre-populated, delete them.
3. Copy release notes from the `CHANGELOG.md` file you generated in the previous step.
4. Publish the release.

## Trigger Documentation Deployment

Documentation for the respective release branch needs to be triggered manually after the release branch has been pushed.

1. Navigate to the [Generate and push docs](https://github.com/kcp-dev/kcp/actions/workflows/docs-gen-and-push.yaml) GitHub Action.
2. Hit the "Run forkflow" button, run workflow against `release-$VERSION`.
3. Make sure the triggered workflow ran and deployed a new version of the documentation to [docs.kcp.io](https://docs.kcp.io).

## Notify

1. Create an email addressed to kcp-dev@googlegroups.com and kcp-users@googlegroups.com
   1. Subject: `[release] <version>` e.g. `[release] v0.8.0`
   2. In the body, include noteworthy changes
   3. Provide a link to the release in GitHub for the full release notes
2. Post a message in the [#kcp-users](https://kubernetes.slack.com/archives/C021U8WSAFK) Slack channel
