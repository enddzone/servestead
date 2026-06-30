// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

const site = process.env.DOCS_SITE_URL ?? 'https://docs.servestead.com';
const base = process.env.DOCS_BASE_PATH ?? '/';

export default defineConfig({
  site,
  base,
  integrations: [
    starlight({
      title: 'Servestead',
      description: 'Beginner-friendly docs for turning a raw Ubuntu VPS into a hardened, Git-backed server homestead.',
      customCss: ['./src/styles/custom.css'],
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/enddzone/servestead',
        },
      ],
      editLink: {
        baseUrl: 'https://github.com/enddzone/servestead/edit/main/docs/',
      },
      sidebar: [
        {
          label: 'Start Here',
          items: [
            { label: 'Overview', slug: 'getting-started' },
            { label: 'Prerequisites', slug: 'getting-started/prerequisites' },
            { label: 'Build the CLI', slug: 'getting-started/build' },
            { label: 'Use an existing VPS', slug: 'getting-started/existing-vps' },
            { label: 'Provision a VPS', slug: 'getting-started/provision-vps' },
          ],
        },
        {
          label: 'Guides',
          items: [
            { label: 'Guided setup', slug: 'guides/guided-setup' },
            { label: 'DNS and proxy', slug: 'guides/dns-and-proxy' },
            { label: 'Observability', slug: 'guides/observability' },
            { label: 'Add an application stack', slug: 'guides/add-stack' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Command reference', slug: 'reference/commands' },
            { label: 'Security model', slug: 'reference/security-model' },
          ],
        },
        {
          label: 'Troubleshooting',
          items: [{ label: 'Common issues', slug: 'troubleshooting' }],
        },
      ],
    }),
  ],
});
