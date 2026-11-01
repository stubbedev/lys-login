# NixOS

Add the flake as an input and import the module:

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    lys-login = {
      url = "github:stubbedev/lys-login";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };
  outputs = {nixpkgs, lys-login, ...}: {
    nixosConfigurations.host = nixpkgs.lib.nixosSystem {
      modules = [
        lys-login.nixosModules.default
        ./configuration.nix
      ];
    };
  };
}
```

## The display manager

`enable` alone is a working setup. It runs `lys-login daemon` as
`display-manager.service`, showing the greeter in the lys-wm of lys-login's flake
input, and adds the `lys-login` PAM service, the `lys-greeter` system user and
the state directory, with neither greetd nor cage.

```nix
programs.lys-login.greeter = {
  enable = true;
  # Theme it like your desktop: the full lys-shell config schema. The greeter
  # honours styling, login.* and wallpaper.
  settings = {
    styling.appearance = "dark";
    login.background-mode = "color";
    login.background-color = "#1e1e2e";
    login.greeter.cursor-size = 24;
  };
};
```

`settings` becomes `/etc/lys-shell/config.yaml`, and
[Configuration](/guide/configuration) covers what it holds.

Sessions come from the aggregate NixOS sessions directory, which holds the session
files of every installed compositor. The unit does not restart on
`nixos-rebuild switch`, which would end your session; the new one starts at the
next boot.

To show the greeter in the lys-wm your desktop runs:

```nix
programs.lys-login.greeter.compositorPackage = config.programs.lys-wm.package;
```

## The lock screen

```nix
programs.lys-login = {
  enable = true;
  lock.enable = true;
};
```

`enable` puts `lys-login` on the PATH, which `greeter.enable` does too.
`lock.enable` adds the [PAM service](/guide/pam#the-lock-screen-s-service) the
unlock needs. Set `login.lock.pam-service` in your lys-shell config to match
`lock.pamService`, `lys-shell` by default.

## With lys-splash

With [lys-splash](https://github.com/stubbedev/lys-splash) as the boot splash, set
`greeter.splashPackage` to it. The greeter then takes the screen from the splash
without a blank frame, as GDM does from plymouth: the unit conflicts with
`lys-splash-quit.service` and runs `lys-splash deactivate` before lys-wm starts,
which keeps the splash's frame, and the daemon runs
`lys-splash quit --retain-splash` once the greeter's first frame is presented.

## Under greetd

`greeter.displayManager = "greetd"` runs the greeter under greetd in a cage kiosk
instead, with its state owned by greetd's `greeter` user.

## Options

All under `programs.lys-login`.

| Option | Default | What it does |
| --- | --- | --- |
| `enable` | `false` | `lys-login` on the PATH. |
| `package` | this flake's | The lys-login package. |
| `lock.enable` | `false` | Declares the lock screen's PAM service. |
| `lock.pamService` | `"lys-shell"` | Its name. |
| `greeter.enable` | `false` | The greeter, run by `greeter.displayManager`. |
| `greeter.displayManager` | `"lys-login"` | `lys-login`: `lys-login daemon` on `greeter.vt`. `greetd`: greetd with a cage kiosk. |
| `greeter.compositorPackage` | lys-login's lys-wm input | The lys-wm the daemon shows the greeter in. |
| `greeter.splashPackage` | `null` | lys-splash, when it is the boot splash. |
| `greeter.vt` | `1` | The VT the daemon runs on. |
| `greeter.settings` | `{}` | Written to `/etc/lys-shell/config.yaml` when set. |
| `greeter.session.dirs` | NixOS sessions dir | Directories scanned for Wayland `*.desktop` session files. |
| `greeter.session.x11Dirs` | NixOS xsessions dir | The same for X11, launched through `startx`; `[]` disables them. `startx` must be on the session's PATH, typically through `services.xserver.enable`. |
| `greeter.session.command` | `""` | An explicit fallback session, offered as "Custom", and the only entry when the directories yield none. |
| `greeter.session.environment` | `[]` | Extra `KEY=value` entries for the started session. |
| `greeter.cagePackage` | `pkgs.cage` | Under greetd: the kiosk compositor. |
| `greeter.renderer` | `"auto"` | Under greetd: `software` puts cage on its pixman renderer. The display manager takes only `auto`, as lys-wm falls back to software itself. |
| `greeter.graphicsWrapper` | `[]` | Under greetd: a command prefix around the whole launch, such as a nixGL wrapper. The display manager takes none. |

The module refuses to evaluate when no session can be offered, or when `renderer`
or `graphicsWrapper` are set under the display manager.
