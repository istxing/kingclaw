# Fork Maintenance Guide

This repository can be maintained as a long-lived fork while still following upstream updates.

## Remote Layout

- `origin`: your fork repository
- `upstream`: official repository (`sipeed/picoclaw`)

If your `origin` still points to the official repo, switch it to your fork URL:

```bash
git remote set-url origin <your-fork-url>
```

## Recommended Branch Model

- `main`: your maintained branch (rebased on upstream regularly)
- `feature/*`: work branches for local fixes

## Sync Upstream

Use the script:

```bash
./scripts/sync_upstream.sh
```

Default behavior:

1. fetches `upstream`
2. checks out `main`
3. rebases `main` onto `upstream/main`
4. prints next-step push commands

## Patch Discipline

Track local-only patches in `PATCHES.md`.

When a patch is merged upstream:

1. sync from upstream
2. remove corresponding entry from `PATCHES.md`

## Upstream Contribution Flow

For local patches worth contributing:

1. create focused branch from `main`
2. keep patch small and tested
3. open upstream PR
4. once merged, drop local delta
