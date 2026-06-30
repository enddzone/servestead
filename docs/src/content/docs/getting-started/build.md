---
title: Build the CLI
description: Build Servestead from source before running setup commands.
---

From the repository root, build the CLI:

```sh
go build -o bin/servestead .
```

Confirm the binary exists:

```sh
bin/servestead --help
```

## Run Tests

Before changing the CLI, run:

```sh
go test ./...
```

Provider provisioning is billable and is not run by the test suite. Cloud API clients are tested against local HTTP servers.

## Local Docs Site

The docs site is separate from the Go CLI:

```sh
cd docs
npm install
npm run dev
```

Use `npm run build` to verify the static docs output.
