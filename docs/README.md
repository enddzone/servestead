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

The repository workflow builds this site with `DOCS_BASE_PATH=/` and `DOCS_SITE_URL=https://docs.servestead.com`, matching the configured GitHub Pages custom domain.

If the custom domain is removed and the site is served from the repository project path instead, set `DOCS_BASE_PATH=/servestead` and `DOCS_SITE_URL=https://enddzone.github.io`.
