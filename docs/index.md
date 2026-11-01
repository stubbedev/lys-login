---
layout: home

hero:
  name: lys-login
  text: Log in and lock, one screen
  tagline: The lys desktop's display manager, greeter and lock screen in pure Go. The display manager needs PAM and lys-wm, with no greetd, cage or polkit.
  image:
    src: /lys-login.svg
    alt: lys-login
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: Install
      link: /install/nixos
    - theme: alt
      text: GitHub
      link: https://github.com/stubbedev/lys-login

features:
  - title: A display manager of its own
    details: A root daemon on a VT shows the greeter in a lys-wm kiosk and starts your session. It is the NixOS module's default and what the .deb and .rpm install.
  - title: One screen, two modes
    details: The greeter and the session lock share the login config section, so they share the theme, background, blur and clock.
  - title: The login unlocks your keyring
    details: The password you log in with opens the login collection of lys-secrets, or a running gnome-keyring, before the first app asks for a secret.
  - title: Workers hold the sessions
    details: Every login runs in a worker process of its own that holds the PAM transaction, and the greeter runs unprivileged on a socket only it can reach.
---
