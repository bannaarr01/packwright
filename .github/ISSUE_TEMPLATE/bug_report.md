---
name: Bug Report
about: Report a bug to help us improve
title: "[Bug] "
labels: bug
assignees: ''
---

## Describe the bug

A clear and concise description of what the bug is.

## Front-end

- [ ] TUI (`packwright`)
- [ ] GUI (`packwright --gui`)
- [ ] Both

## To reproduce

Steps to reproduce the behaviour:

1. Run `...`
2. Press `...`
3. Observe `...`

## Expected behaviour

What you expected to happen.

## Environment

- **Packwright version** (`packwright --version`):
- **Go version** (if building from source):
- **OS / arch**:
- **Shell** (TUI only):
- **Wails version** (GUI only):

## Pack / config layout

If the bug involves the palette, manifest watcher, or conflict
resolution, sketch your `~/.config/packwright/` layout (redact pack
contents you can't share):

```
~/.config/packwright/
├── config.yaml
├── commands/
│   └── ...
└── packs/
    └── ...
```

## Logs

```
Paste relevant output here. Redact AWS account IDs, ARNs, profile names,
and any secrets.
```

## Additional context

- [ ] First-time launch (no existing home)
- [ ] After a manifest edit (hot-reload path)
- [ ] After running a scaffold wizard
- [ ] Other (describe)
