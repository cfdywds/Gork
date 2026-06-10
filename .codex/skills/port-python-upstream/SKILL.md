---
name: port-python-upstream
description: Use when maintaining this repository's Go main branch while tracking Python upstream changes, checking pending upstream commits, translating Python commits into Go behavior, renaming go/main/python branches, or marking already-ported Python commits as absorbed.
---

# Port Python Upstream

## Core Model

Use Git history as the source of truth for upstream porting state, but do not confuse ancestry bookkeeping with code migration:

- `main` is the Go product branch and must not track `upstream/main`.
- `python` is the Python upstream mirror and should track `upstream/main`.
- `python` should stay clean: update it with `git merge --ff-only upstream/main`.
- `main..python` lists Python upstream commits whose ancestry has not yet been absorbed by Go.
- Absorbing ancestry is not the same as porting behavior. A Python commit is not "done" until its applicable behavior, config, docs, security fix, tests, or version change has been translated into the Go tree or explicitly classified as intentionally irrelevant.
- Only after real porting and verification may `main` use an `ours` merge to mark the corresponding Python commit or contiguous range as absorbed without copying Python runtime files into the Go tree.

## Required Safety Checks

Before branch renames, pushes, or merges:

1. Check `git status --short` and do not overwrite user work.
2. Check `git remote -v`, `git branch -vv`, and the current branch.
3. Distinguish local branch changes from remote/default-branch changes.
4. Ask before force-pushing, deleting branches, changing remote defaults, or rewriting published history.

Do not merge Go commits into `python`. Do not use regular merge from `python` into `main` for porting markers.

Never use `git merge -s ours python` as the whole solution to "merge Python changes into main". That only records ancestry and keeps the Go tree unchanged. It is valid only as the final bookkeeping step after the Python changes have already been translated, implemented, and verified in Go.

## Initial Branch Shape

When converting the current repository layout:

```powershell
git branch -m main python
git branch -m go main

git switch python
git branch --set-upstream-to=upstream/main python

git switch main
git branch --unset-upstream
```

Then verify:

```powershell
git branch -vv
git log --oneline main..python
```

Remote changes are separate. If the user wants `origin/main` to become Go `main`, confirm first, then use a guarded push such as `--force-with-lease` only when appropriate.

## Routine Sync Workflow

Update the Python mirror:

```powershell
git fetch upstream
git switch python
git merge --ff-only upstream/main
```

Find pending upstream work:

```powershell
git switch main
git log --oneline main..python
```

For each pending commit or contiguous range, inspect the actual diff and classify every changed behavior:

| Class | Action |
| --- | --- |
| Go behavior needed | Translate the behavior into Go code, add or update Go tests first when practical, then run targeted tests |
| Security/dependency/runtime fix | Apply the equivalent fix to the Go runtime, Docker files, configs, or dependency metadata if applicable |
| Docs/assets/config still applicable | Manually port into the Go project shape; do not blindly copy Python-only docs or paths |
| Python-only runtime | Skip implementation only with an explicit reason recorded in the commit message or notes |
| Unknown | Inspect the Python diff and matching Go owner before deciding |

## Porting Workflow

For each commit or contiguous range:

1. Inspect Python diff from `python`.
2. Map each changed Python file or behavior to the Go owner on `main`.
3. Decide whether each change is applicable, already present, needs a Go translation, or is intentionally Python-only.
4. Add or update Go tests first when behavior changes and the test surface exists.
5. Implement the Go equivalent without importing Python-only runtime code or replacing the Go product tree with Python files.
6. Run targeted tests, then `go test ./...` when practical.
7. Commit the Go migration on `main` with a message that names the upstream Python commit or range.
8. Only after steps 1-7 are complete, mark the Python commit or contiguous range as absorbed with an `ours` merge on `main`.

Example:

```powershell
git switch main
git show --stat <python-sha>
git show <python-sha> -- <relevant-python-files>
# edit Go code, docs, config, and tests with the equivalent behavior
go test ./app/products/web/admin ./app/dataplane/reverse/protocol -count=1
git commit -m "fix(auth): port Python upstream <python-sha> auth flow"

git merge --no-ff -s ours <python-sha> -m "chore(upstream): mark Python upstream <python-sha> as absorbed after Go port"
git log --oneline main..python
```

`-s ours` records the merge relationship but keeps the Go tree content from `main`.

## Important Limitation

Git merge ancestry is transitive. If you `ours` merge a later Python commit, Git also treats its ancestors as merged. Therefore:

- Prefer processing pending Python commits in order.
- If skipping a middle commit, explicitly decide whether its ancestors should also be considered absorbed.
- For a batch, only use an `ours` merge at the end of a contiguous range when every commit in that range has been ported or intentionally skipped.

## Common Mistakes

- Regular-merging `python` into `main`: this can bring Python runtime files into the Go branch.
- Using `ours` merge before porting: this makes `main..python` empty while silently dropping the requested Python behavior.
- Updating `python` with non-fast-forward local commits: this destroys its value as an upstream mirror.
- Treating `main..python` as "not implemented" after doing local Go code but before the `ours` marker: the implementation and marker must both happen.
- Marking a commit absorbed before tests show the Go behavior matches.
- Saying "merged" when only ancestry changed. Report this as "marked absorbed" unless code/docs/config changes were actually ported into `main`.
