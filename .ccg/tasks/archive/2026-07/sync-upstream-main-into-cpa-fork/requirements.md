# Requirements

Sync upstream `router-for-me/CLIProxyAPI` main into local `main`, then merge it into fork branch `CPA-fork` while preserving CPA fork customizations.

Must preserve:
- Fork management UI repository defaults.
- Request log page/backend behavior and model badge logging.
- Request logger hot-reload behavior added in CPA fork.
- AI provider usage/details behavior backed by request logs.
- Plugin management compatibility with upstream plugin updates.

Local user changes stashed before sync must not be mixed into the upstream merge commit.
