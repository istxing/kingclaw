#!/usr/bin/env bash
set -euo pipefail

BASE_BRANCH="${1:-main}"

if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "error: not inside a git repository" >&2
  exit 1
fi

if ! git remote get-url upstream >/dev/null 2>&1; then
  echo "error: missing 'upstream' remote" >&2
  echo "hint: git remote add upstream https://github.com/sipeed/picoclaw.git" >&2
  exit 1
fi

echo "[1/4] fetching upstream..."
git fetch upstream

echo "[2/4] checking out ${BASE_BRANCH}..."
git checkout "${BASE_BRANCH}"

echo "[3/4] rebasing ${BASE_BRANCH} onto upstream/${BASE_BRANCH}..."
git rebase "upstream/${BASE_BRANCH}"

echo "[4/4] done."
echo
echo "Next:"
echo "  git push --force-with-lease origin ${BASE_BRANCH}"
echo "  # or open a PR from ${BASE_BRANCH} in your fork workflow"
