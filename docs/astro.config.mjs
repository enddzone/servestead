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
      description: 'Guides for provisioning, hardening, and operating a Git-backed Ubuntu VPS with Servestead.',
      logo: {
        src: './src/assets/servestead-mark.svg',
        alt: '',
      },
      customCss: ['./src/styles/custom.css'],
      head: [
        {
          tag: 'meta',
          attrs: { name: 'theme-color', content: '#11111b' },
        },
      ],
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
          label: 'Start here',
          items: [
            { label: 'Overview', slug: 'getting-started' },
            { label: 'Requirements', slug: 'getting-started/prerequisites' },
            { label: 'Install and launch', slug: 'getting-started/build' },
            { label: 'Connect an existing VPS', slug: 'getting-started/existing-vps' },
            { label: 'Provision with DigitalOcean', slug: 'getting-started/provision-vps' },
          ],
        },
        {
          label: 'Terminal workflow',
          items: [
            { label: 'Profile dashboard', slug: 'guides/command-center' },
            { label: 'Guided setup', slug: 'guides/guided-setup' },
            { label: 'Profiles and run history', slug: 'guides/profiles-and-diagnostics' },
          ],
        },
        {
          label: 'Deploy and operate',
          items: [
            { label: 'DNS and proxy', slug: 'guides/dns-and-proxy' },
            { label: 'Add an application stack', slug: 'guides/add-stack' },
            { label: 'GitOps review and sync', slug: 'guides/gitops' },
            { label: 'Observability', slug: 'guides/observability' },
            { label: 'Access and secrets', slug: 'guides/access-and-secrets' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Terminal UI', slug: 'reference/terminal-ui' },
            { label: 'CLI commands', slug: 'reference/commands' },
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
