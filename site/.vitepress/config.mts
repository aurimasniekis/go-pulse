import { defineConfig } from 'vitepress';

const repo = 'https://github.com/aurimasniekis/go-pulse';

export default defineConfig({
  title: 'Pulse',
  description:
    'A personal, durable event bus on NATS JetStream: least-privilege clients, years of replayable history, webhooks, bans, backups and an operator dashboard.',
  lang: 'en-US',
  // GitHub Pages project site: https://aurimasniekis.github.io/go-pulse/
  base: '/go-pulse/',
  cleanUrls: true,
  lastUpdated: false,
  srcExclude: ['README.md'],
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/go-pulse/logo.svg' }],
    ['meta', { name: 'theme-color', content: '#4750d6' }],
  ],
  markdown: {
    theme: { light: 'github-light', dark: 'github-dark' },
  },
  themeConfig: {
    logo: '/logo.svg',
    nav: [
      { text: 'Get started', link: '/guide/what-is-pulse' },
      { text: 'Clients & SDKs', link: '/clients/' },
      { text: 'Operators', link: '/operators/' },
      { text: 'Security', link: '/security/' },
      { text: 'Reference', link: '/reference/configuration' },
    ],
    sidebar: [
      {
        text: 'Get started',
        items: [
          { text: 'What is Pulse?', link: '/guide/what-is-pulse' },
          { text: 'Concepts', link: '/guide/concepts' },
          { text: 'Try it locally', link: '/guide/try-locally' },
          { text: 'Deploy on a VPS', link: '/guide/deploy' },
          { text: 'Connect your first tool', link: '/guide/first-client' },
          { text: 'The dashboard', link: '/guide/dashboard' },
        ],
      },
      {
        text: 'Clients & SDKs',
        items: [
          { text: 'Overview', link: '/clients/' },
          { text: 'Subjects and the envelope', link: '/clients/envelope' },
          { text: 'Go SDK', link: '/clients/go' },
          { text: 'Python SDK', link: '/clients/python' },
          { text: 'TypeScript SDK (node and browser)', link: '/clients/typescript' },
          { text: 'pulse CLI', link: '/clients/cli' },
          { text: 'HTTP API', link: '/clients/http' },
          { text: 'Webhooks in and out', link: '/clients/webhooks' },
        ],
      },
      {
        text: 'Operator guides',
        items: [
          { text: 'Overview', link: '/operators/' },
          { text: 'Clients and grants', link: '/operators/clients' },
          { text: 'Streams, KV and objects', link: '/operators/streams' },
          { text: 'Cloudflare and TLS', link: '/operators/tls' },
          { text: 'Firewall and bans', link: '/operators/bans' },
          { text: 'Keys and credentials', link: '/operators/keys' },
          { text: 'Backup and restore', link: '/operators/backups' },
          { text: 'Monitoring and alerts', link: '/operators/monitoring' },
          { text: 'Upgrades', link: '/operators/upgrades' },
        ],
      },
      {
        text: 'Security',
        items: [{ text: 'Security model', link: '/security/' }],
      },
      {
        text: 'Help',
        items: [
          { text: 'Troubleshooting', link: '/help/troubleshooting' },
          { text: 'FAQ', link: '/help/faq' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Configuration', link: '/reference/configuration' },
          { text: 'Server commands', link: '/reference/pulsed' },
          { text: 'Admin API', link: '/reference/admin-api' },
          { text: 'Metrics', link: '/reference/metrics' },
        ],
      },
    ],
    socialLinks: [{ icon: 'github', link: repo }],
    editLink: {
      pattern: `${repo}/edit/main/site/:path`,
      text: 'Edit this page on GitHub',
    },
    search: { provider: 'local' },
    outline: { level: [2, 3] },
    footer: {
      message: 'Released under the Apache-2.0 license.',
      copyright: 'Pulse contributors',
    },
  },
});
