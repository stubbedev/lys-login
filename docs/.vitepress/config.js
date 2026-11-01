import { lysConfig } from 'lys-docs-theme/config'

export default lysConfig({
  name: 'lys-login',
  description: "The lys desktop's display manager, greeter and lock screen.",
  nav: [
    { text: 'Guide', link: '/guide/getting-started' },
    { text: 'Install', link: '/install/nixos' },
    { text: 'Reference', link: '/reference/architecture' },
  ],
  sidebar: [
    {
      text: 'Guide',
      items: [
        { text: 'Getting started', link: '/guide/getting-started' },
        { text: 'Configuration', link: '/guide/configuration' },
        { text: 'PAM services', link: '/guide/pam' },
        { text: 'The lock screen', link: '/guide/lock-screen' },
      ],
    },
    {
      text: 'Install',
      items: [
        { text: 'NixOS', link: '/install/nixos' },
        { text: 'Debian and Ubuntu', link: '/install/debian' },
        { text: 'Fedora', link: '/install/fedora' },
        { text: 'Other systems', link: '/install/other' },
      ],
    },
    {
      text: 'Reference',
      items: [
        { text: 'How it fits together', link: '/reference/architecture' },
        { text: 'Command line', link: '/reference/cli' },
        { text: 'Development', link: '/reference/development' },
      ],
    },
  ],
})
