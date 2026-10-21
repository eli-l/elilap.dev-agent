---
type: Run
title: Run capture format
description: Format and conventions for timestamped validator-run capture files in docs/runs/.
status: stable
generated:
  by: z-ai/glm-5.3-flashx
  at: 2026-10-21T21:34:39Z
tags: [runs, meta, validation]
---

Concepts for recording timestamped validator runs in this bundle. One
file per run, named `<topic>-<date>-<slug>.md`.

## Format

A run capture file is named `<topic>-<date>-<slug>.md` and carries OKF
v0.2 frontmatter. A `run:` value is a versioned tool invocation — a
`type` and `cmd` — together with an `output:` value capturing the tool's
exit code and trimmed stdout/stderr. The optional `result` value
(paragraph or list) holds the reviewer's interpretation; commands whose
observed result and frontmatter are human-verified may carry a
`verified:` audit entry.

```yaml
---
type: Run
title: <Human title>
description: <What the run checked, one sentence.>
status: stable
generated:
  by: <actor>
  at: <iso8601 UTC>
verified:                      # only when a human confirmed the output
  - by: human:eli
    at: <iso8601 UTC>
run:
  type: <go-test|go-build|check-ddl|...>
  cmd: <exact command line>
output: |
  <trimmed stdout/stderr; exit code noted>
---

# Result

<Optional reviewer notes>
```

## Conventions

- Never paste credentials or environment values into `output:` — commands
  here are validators, but check before pasting anything the shell echoed.
- Link every new run file from `docs/index.md` under the `# Runs` heading
  and add a `Creation` entry to `docs/log.md` (newest first).
