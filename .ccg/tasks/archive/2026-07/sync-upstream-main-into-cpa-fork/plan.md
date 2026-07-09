# Plan

1. Fetch upstream and fast-forward local `main` to `upstream/main`.
2. Push updated `main` to fork `origin/main`.
3. Merge updated `main` into `CPA-fork`.
4. Resolve conflicts by keeping both upstream improvements and CPA fork invariants.
5. Run focused tests around conflict areas and compile check when network/toolchain allows.
6. Commit merge to `CPA-fork` and push.
7. Archive this CCG task.
