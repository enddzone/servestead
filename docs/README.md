# Servestead Docs Site

This directory contains the Astro Starlight documentation site for Servestead.

## Run Locally

```sh
npm ci
npm run dev
```

The dev server prints the local URL. Content lives under `src/content/docs/`; screenshot-bearing guides use MDX and the shared `src/components/UiScreenshot.astro` component.

Product screenshots live in `src/assets/screenshots/` so Astro can resize, fingerprint, and optimize them. Capture the current UI with sanitized example data, exclude tokenized URLs and revealed credentials, and write captions that explain the user outcome.

## Build

```sh
npm run build
DOCS_BASE_PATH=/servestead DOCS_SITE_URL=https://enddzone.github.io npm run build
```

The static site is written to `dist/`. Run both builds when changing internal links or image paths so the custom-domain and repository-path variants remain valid.

## GitHub Pages

The repository workflow builds this site with `DOCS_BASE_PATH=/` and `DOCS_SITE_URL=https://docs.servestead.com`, matching the configured GitHub Pages custom domain.

If the custom domain is removed and the site is served from the repository project path instead, set `DOCS_BASE_PATH=/servestead` and `DOCS_SITE_URL=https://enddzone.github.io`.
