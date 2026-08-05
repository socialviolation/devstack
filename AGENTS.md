# Agent Instructions

## Landing the plane (session completion)

When you end a work session, work is not complete until every change is committed and its state is reported. Steps:

1. File issues for anything that needs follow-up.
2. Run the quality gates if code changed: tests, linters, build.
3. Update issue status: close what is finished, update what is in progress.
4. Commit everything, on the branch you were working on. Clear your stashes. Nothing you wrote is left uncommitted.
5. Push only where pushing is already the agreed flow for that branch: one that already tracks a remote, or one the human asked you to push. A feature stack's branch lives in its own worktree and is not that by default, so ask first, and say what you would push and where.
6. Hand off with context for the next session, naming anything still unpushed.

Rules:

- Never leave work stranded. Uncommitted or stashed at the end of a session is a failure.
- Pushing a branch nobody asked you to push is also a failure. Ask, then push.
- Never force-push, `--force-with-lease` included, without being asked in that session.
- If a push fails, stop and report the error. Do not retry your way through it.
- Merging is a separate decision and always the human's. Ask before you merge a branch into base or open a PR; never merge unilaterally.
