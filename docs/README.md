# Servestead Docs Site

This directory contains the Astro Starlight documentation site for Servestead.

## Run Locally

```sh
npm install
npm run dev
```

The dev server prints the local URL. Most contributors only need Markdown files under `src/content/docs/`.

## Build

```sh
npm run build
```

The static site is written to `dist/`.

## GitHub Pages

The repository workflow builds this site with `DOCS_BASE_PATH=/AegisNode`, which matches the public repository path `https://enddzone.github.io/AegisNode/`.

If a custom domain is configured for GitHub Pages, update the docs workflow to set `DOCS_BASE_PATH=/` and `DOCS_SITE_URL=https://<custom-domain>`.
