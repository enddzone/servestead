# Operations

This page covers CI/CD pipelines, release management, linting, security scanning, and the documentation site.

## CI/CD Workflows

All workflows are in `.github/workflows/`.

### ci.yml — Go Quality Gate

**Triggers**: PR, push to main, manual.

**Jobs**:
1. **Test**: checkout → setup Go (from `go.mod`) → `templ generate` + verify clean diff → check frontend asset files exist → install golangci-lint v2.11.4 → `golangci-lint fmt` check → `golangci-lint lint` → `go vet` → `go test -race` → upload coverage profile as artifact
2. **Sonar** (needs test): downloads coverage, runs SonarCloud analysis (gated to non-fork PRs)

### codeql.yml — CodeQL Static Analysis

**Triggers**: PR, push to main, weekly cron (Mon 9:32), manual.

Initializes CodeQL DB for Go, builds (`templ generate` + `go build`), runs analysis, uploads security events.

### docs.yml — Docs Build & Deploy

**Triggers**: PR (docs/** paths), push to main (docs/**), manual.

Builds the Astro Starlight site with `DOCS_BASE_PATH=/`. On non-PR events, uploads a GitHub Pages artifact. On main push, deploys to GitHub Pages (`pages: write` + `id-token: write`). Concurrency group cancels in-progress runs.

### release-please.yml — Release PR Management

**Triggers**: Push to main, manual.

Runs `googleapis/release-please-action@v5.0.0` with `release-please-config.json` + `.release-please-manifest.json` to maintain a release PR. When a release is created, triggers `release.yml` via `gh workflow run` with the release tag.

### release.yml — Binary & Container Publishing

**Triggers**: Release published event, manual (with tag input).

**Jobs**:
1. **Binaries**: checkout at tag → `templ generate` + verify → install Syft → GoReleaser v2.16.0 → attest binary checksums (build provenance)
2. **GHCR image**: multi-arch (amd64+arm64) Docker build via `Dockerfile.release` → push to `ghcr.io/<owner>/servestead:<tag>` + `:latest` with SBOM → attest image provenance → Trivy image scan (SARIF upload + fail on HIGH/CRITICAL)

### renovate.yml — Dependency Updates

**Triggers**: Daily cron (10:17), manual.

Runs Renovate bot (`renovatebot/github-action@v46.1.17`) against the repo using `renovate.json` config. Gated to the `enddzone/servestead` repo only. Uses `RENOVATE_TOKEN` secret.

### security.yml — Security Scanning

**Triggers**: PR, push to main, weekly cron (Mon 10:11), manual.

**Jobs**:
1. **govulncheck**: Go vulnerability scanner v1.5.0 + frontend asset check
2. **Dependency review**: PR-only, `dependency-review-action`, fails on high-severity vulnerabilities
3. **Trivy filesystem scan**: SARIF report upload + fail on HIGH/CRITICAL findings

## Linting

**`.golangci.yml`** — golangci-lint v2 configuration:
- **Formatters**: `gofmt` (with `simplify` + `interface{}` → `any` rewrite), `gci` (standard → default → localmodule import ordering)
- **Linters**: `dupword`, `exptostd`, `fatcontext`, `gocognit` (min complexity 16), `loggercheck`, `mirror`, `misspell` (US locale), `thelper`, `usestdlibvars`, `usetesting`
- **Exclusions**: `comments`, `common-false-positives`, `legacy`, `std-error-handling` presets
- **Run**: 5m timeout, tests enabled

Per `AGENTS.md`, always run `golangci-lint` after making changes, in addition to targeted tests or broader test suite runs.

## Release Process

1. **Release Please** maintains a release PR that accumulates conventional commits
2. When the release PR is merged, Release Please creates a tag and GitHub release
3. The release event triggers `release.yml` which builds binaries (GoReleaser) and container images (Docker)
4. Binaries: linux/darwin/windows × amd64/arm64, CGO disabled, `-trimpath`, tar.gz (zip for Windows), with SBOMs and build provenance attestation
5. Container: `ghcr.io/<owner>/servestead:<tag>` + `:latest`, multi-arch, with SBOM and Trivy scan

**Config files**:
- `release-please-config.json`: Go release type, package "servestead", changelog at `CHANGELOG.md`
- `.release-please-manifest.json`: Current version `0.2.1`
- `.goreleaser.yaml`: Build config, archive templates, checksums, SBOMs

**Current version**: 0.2.1 (from `CHANGELOG.md`)

## Renovate Configuration

**`renovate.json`**:
- Extends `config:recommended`
- Semantic commits (`chore` type), America/Chicago timezone, morning schedule
- Groups minor/patch updates as "non-major dependencies"; GitHub Actions updates grouped separately
- Custom regex managers track `GOVULNCHECK_VERSION`, `GORELEASER_VERSION`, `SYFT_VERSION`, `TRIVY_VERSION` in workflow YAML
- Go module updates include import path rewriting + `gomodTidy`
- PR limits: 2/hour, 5 concurrent

## SonarCloud

**`sonar-project.properties`**:
- Project key: `enddzone_servestead`, organization: `enddzone`
- Sources: `.`, tests: `backend/**/*_test.go`
- Exclusions: `.git`, `.github`, `.lavish`, `bin`, `dist`, `coverage.out`, test files, `docs`, `mockups`, `openwiki`
- Coverage exclusions: `backend/setup.go`, `frontend/**`

## Documentation Site

**`docs/`** — Astro Starlight documentation site (Astro `^7.0.3`, Starlight `^0.41.1`, Node ≥20.19).

**Structure** (`docs/src/content/docs/`):
- `getting-started/` — overview, prerequisites, build, existing-VPS, provision-VPS
- `guides/` — guided setup, DNS & proxy, observability, add stack
- `reference/` — commands, security model
- `troubleshooting/` — common issues

**Config** (`docs/astro.config.mjs`):
- Title: "Servestead", custom CSS
- GitHub social link, edit link to repo `docs/` directory
- `site` and `base` configurable via `DOCS_SITE_URL` and `DOCS_BASE_PATH` env vars (defaults: `https://docs.servestead.com` and `/`)

**Deployment**: GitHub Pages via `docs.yml` workflow. Supports custom domain (`docs.servestead.com`) or project-path mode (`/servestead`).

## Docker Release Image

**`Dockerfile.release`** — Multi-stage build for the release container image. Built and pushed by `release.yml` to GHCR with SBOM, provenance attestation, and Trivy scanning.

## Other Configuration Files

- `.dockerignore` — Excludes non-essential files from Docker build context
- `.gitignore` — Standard Go ignores (`bin/`, coverage, etc.)
- `.codex/environments/environment.toml` — Codex AI agent environment descriptor
- `.lavish/servestead-web-phased-plan.html` — UI design/planning document
