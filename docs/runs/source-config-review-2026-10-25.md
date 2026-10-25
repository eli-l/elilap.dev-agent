---
type: Run
title: Source config review verification
description: Offline checks of review fixes for PR 3.
status: stable
generated:
  by: openai/gpt-6
  at: 2026-10-25T03:33:51Z
tags: [config, review, validation]
run:
  type: go-test
  cmd: go build ./... && go vet ./... && go test -count=1 -json ./...
output: |
  Go 1.27.0 invoked from the installed toolchain with GOTOOLCHAIN=local.
  Build, vet and tests: exit 0.
  13 top-level tests and 47 subtests passed across three packages.
  Ghost and Postiz option packages have no tests.
  gofmt -l cmd internal migrations: no output.
  sources list: exit 0; grep of combined output for four fake values: 0 matches.
  sources validate --strict: all credentials set exit 0; one missing exit 1.
  sources validate: one missing warns and exits 0.
  sources list --strict: exit 2 (flag belongs to validate only).
  PLAN schema matches migration after removing comments and IF NOT EXISTS.
  OKF binaries unavailable; manual frontmatter, index/log and links checked.
---

No live API requests were used. Automatic toolchain selection could not
complete offline; checks used the installed Go 1.27.0 executable directly.
