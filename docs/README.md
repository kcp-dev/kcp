# kcp Documentation Setup

## Overview

Our documentation is powered by [Zensical](https://zensical.org/) (Material for MkDocs compatible theme) with some
additional tools:

- [awesome-nav](https://lukasgeiter.github.io/mkdocs-awesome-nav/) (native Zensical implementation) for navigation,
  configured via `.nav.yml` files
- [mike](https://github.com/squidfunk/mike) (Zensical-compatible fork) for multiple version support

Dependencies are managed with [uv](https://docs.astral.sh/uv/) (`pyproject.toml`, `uv.lock`).

## File structure

All documentation-related items live in `docs` (with the small exception of various `make` targets and some helper
scripts in `hack`).

| Path                | Description                                                                 |
|---------------------|-----------------------------------------------------------------------------|
| content             | Website content. Navigation is configured with `.nav.yml` files.            |
| generated           | Generated site. Never added to git.                                         |
| overrides           | Theme overrides, stylesheets, scripts and static assets.                    |
| generators          | Generators for CLI and API reference docs.                                  |
| mkdocs.yml          | Zensical configuration (also read by `mike`).                               |
| pyproject.toml      | Python dependencies used to build the site.                                 |

## Local preview

```sh
make local-docs
```

## Publishing Workflow

All documentation building and publishing is done using GitHub Actions in
[docs-gen-and-push.yaml](../.github/workflows/docs-gen-and-push.yaml). The overall sequence is:

1. Generate CLI docs
2. Generate API docs
3. Run `mike deploy --push`, which builds the site with Zensical

## Theme Overrides

We override `partials/outdated.html` to customize the outdated version warning.
