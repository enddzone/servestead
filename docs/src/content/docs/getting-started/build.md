---
title: Install and Launch
description: Build Servestead from source and open its local web interface.
---

Build the current Servestead source, confirm the CLI, and open the local control plane.

## 1. Build the CLI

From the repository root:

```sh
mkdir -p bin
go build -o ./bin/servestead ./backend
```

Confirm the binary is available:

```sh
./bin/servestead --help
```

## 2. Run the Preflight

```sh
./bin/servestead doctor
```

Resolve missing local requirements before connecting to a server.

## 3. Launch Servestead Web

```sh
./bin/servestead ui
```

Servestead binds to a random local loopback port, prints the session URL, and opens it in your default browser. Keep this terminal process running while you use the interface.

### If the Browser Does Not Open

```sh
./bin/servestead ui --no-open
```

Copy the printed `http://127.0.0.1:…/ui?token=…` URL into a browser on the same machine. Treat that URL as a temporary session credential and do not share it.

### Use a Predictable Local Port

```sh
./bin/servestead ui --addr 127.0.0.1:8080 --no-open
```

The address must use `localhost`, `127.0.0.1`, or `::1`. Servestead rejects non-loopback bindings.

## 4. Close the Session

Use **Shutdown** in the left navigation, or return to the terminal and press `Ctrl+C`. Closing only the browser tab does not stop the local process.

### Expected Result

The Command Center opens. On a first run it shows **No profiles yet** and links to **Start setup**. On later runs it selects the most recently updated profile.

Continue with [Connect an existing VPS](../existing-vps/) or [Provision with DigitalOcean](../provision-vps/).

:::note[Contributor checks]
Development tests and docs-site commands live in the repository README and `docs/README.md`; they are not required for normal operator setup.
:::
