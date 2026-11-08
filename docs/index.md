---
layout: home

hero:
  name: lys-login
  text: Log in and lock, one screen
  tagline: The lys desktop's display manager, greeter and lock screen in pure Go. Needs PAM and lys-wm; no greetd, cage or polkit.
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
  - title: Its own display manager
    details: A root daemon on a VT shows the greeter in a lys-wm kiosk and starts your session through PAM.
  - title: One screen, two modes
    details: The greeter and the lock screen share the login config section, theme, background and clock.
  - title: Opens your keyring
    details: The login password unlocks the login collection of lys-secrets, or gnome-keyring.
  - title: Isolated workers
    details: Each login runs in its own worker holding the PAM transaction; the greeter runs unprivileged.
---
