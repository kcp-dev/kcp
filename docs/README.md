# kcp Documentation Setup

## Overview

Our documentation is powered by [Material for MkDocs](https://squidfunk.github.io/mkdocs-material/) with some
additional plugins and tools:

- [awesome-pages plugin](https://github.com/lukasgeiter/mkdocs-awesome-pages-plugin)
- [macros plugin](https://mkdocs-macros-plugin.readthedocs.io/en/latest/)
- [mike](https://github.com/jimporter/mike) for multiple version support

We have support in place for multiple languages (i18n), although we currently only have documentation in English. If
you're interested in contributing translations, please let us know!

## File structure

All documentation-related items live in `docs` (with the small exception of various `make` targets and some helper
scripts in `hack`).

The structure of `docs` is as follows:

| Path                        | Description                                                                       |
|-----------------------------|-----------------------------------------------------------------------------------|
| config/$language/mkdocs.yml | Language-specific `mkdocs` configuration.                                         |
| content/$language           | Language-specific website content.                                                |
| generated/branch            | All generated content for all languages for the current version.                  |
| generated/branch/$language  | Generated content for a single language. Never added to git.                      |
| generated/branch/index.html | Minimal index for the current version that redirects to the default language (en) |
| overrides                   | Global (not language-specific) content.                                           |
| Dockerfile                  | Builds the kcp-docs image containing mkdocs + associated tooling.                 |
| mkdocs.yml                  | Minimal `mkdocs` configuration for `mike` for multi-version support.              |
| requirements.txt            | List of Python modules used to build the site.                                    |

## Publishing Workflow

All documentation building and publishing is done using GitHub Actions in
[docs-gen-and-push.yaml](../.github/workflows/docs-gen-and-push.yaml). The overall sequence is:

1. Generate CLI docs
2. Generate API docs
3. Run `mkdocs build` for all languages
4. Run `mike deploy --push`

## Theme Overrides

The goal is to have no full overrides of any theme partials. We currently override `header.html` to customize the
way the language selection dropdown is rendered. We are working with the theme developers to see if we can help make
some changes upstream that don't require an override.
