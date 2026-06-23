# PR Approver

Bulk-review and approve GitHub PRs requesting your review. Fetches GitHub notifications for review-requested PRs, groups changes by content hash, and provides CLI and TUI interfaces to approve or decline them.

## Prerequisites

- Go 1.25+
- A GitHub personal access token with `repo` and `notifications` scopes

```bash
export GITHUB_TOKEN=ghp_...
```

## Installation

```bash
go install github.com/mallendem/gh-pr-review@latest
```

Or build from source:

```bash
git clone https://github.com/mallendem/gh-pr-review.git
cd gh-pr-review
go build -o pr-approver .
```

## Usage

### GUI mode (recommended)

```bash
pr-approver approve gui
```

Opens an interactive TUI where you can review and approve PRs. If `--user` is omitted, a user selection panel is shown first.

```bash
pr-approver approve gui --user alice --propagate --dry-run
```

#### GUI keybindings

| Key | Action |
|---|---|
| `a` / `d` | Move focus left / right between columns |
| `w` / `s` | Scroll up / down in the focused column |
| `tab` | Switch focus between top row and PR body |
| `e` / `r` | Previous / next file tab |
| `alt+a` / `alt+d` | Horizontal scroll in changes column |
| `x` | Approve selected hash |
| `f` | Decline selected hash |
| `c` | Commit (approve staged PRs) — shows confirmation dialog |
| `p` | Open settings panel |
| `q` / `esc` | Quit |

#### GUI columns

1. **Hashes** — content hashes with approval status (checkmark/x)
2. **Changes** — diff view with syntax coloring (`+` green, `-` red) and configurable context lines
3. **Related PRs** — PRs associated with the selected hash, with linked hash tree view
4. **Staged changes** — PRs that are fully approved and ready to commit

### CLI mode

```bash
# Show changes for specific users
pr-approver approve --user alice,bob

# List users with pending reviews
pr-approver approve --only-users

# Approve PRs by hash
pr-approver approve --hash abc123,def456
```

### Manual interactive mode

```bash
pr-approver approve manual --user alice --propagate --dry-run
```

Steps through each hash interactively in the terminal (`y` approve, `n` decline, `s` show PR comment, `q` quit).

## Configuration

Settings can be configured in the GUI via the `p` key, or by creating a config file at `~/.gh-pr-approver`:

```
# Comment to leave on approved PRs
review_comment = This change has been reviewed by a human with a batch tool.

# Number of context lines to show around changes (0 = changes only)
context_lines = 10
```

Supported keys:

| Key | Default | Description |
|---|---|---|
| `review_comment` | `This change has been reviewed by a human with a batch tool.` | Body text for the approval review |
| `context_lines` | `10` | Number of unchanged lines shown around each change in the diff view |

Settings edited in the GUI take effect immediately but are not persisted to the file. To make settings permanent, edit `~/.gh-pr-approver`.

## Flags

| Flag | Commands | Description |
|---|---|---|
| `--user, -u` | `approve`, `gui` | Comma-separated list of GitHub usernames |
| `--hash, -x` | `approve` | Comma-separated list of hashes to approve |
| `--only-users, -o` | `approve` | Print users with pending reviews and exit |
| `--propagate, -p` | `manual`, `gui` | Auto-approve linked hashes in the same PR |
| `--dry-run, -d` | `manual`, `gui` | Print what would be approved without calling the API |

## How it works

1. Fetches your GitHub notifications filtered to `review_requested`
2. For each PR, downloads the diff and splits it into hunks
3. Each hunk is normalized (whitespace-stripped) and SHA-256 hashed
4. Identical changes across PRs share the same hash — review once, approve everywhere
5. When you approve all hashes for a PR, it can be committed: the tool creates an approval review, attempts to rebase the branch, and enables auto-merge (falling back to squash merge)
