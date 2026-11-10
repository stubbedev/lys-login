# NixOS

Add the flake and import the module:

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

## Display manager

```nix
programs.lys-login.greeter = {
  enable = true;
  settings = {
    styling.appearance = "dark";
    login.background-mode = "color";
    login.background-color = "#1e1e2e";
  };
};
```

This runs `lys-login daemon` as `display-manager.service` with the lys-wm from
lys-login's flake input, and adds the `lys-login` and `lys-login-autologin` PAM
services, the `lys-greeter` user and `/var/lib/lys-greeter`. `settings` becomes
`/etc/lys-shell/config.yaml` ([Configuration](/guide/configuration)). Sessions
come from the NixOS sessions directory, which collects every installed
compositor's session file.

`nixos-rebuild switch` never restarts the unit. When it changed, the switch
reloads it, which [upgrades the daemon in place](/reference/architecture#upgrading-in-place).
If the upgrade fails the old daemon keeps running, the switch exits 4 and lists
`lys-login-upgrade-failed.service`, whose status has the reason.

To use the lys-wm your desktop runs:

```nix
programs.lys-login.greeter.compositorPackage = config.programs.lys-wm.package;
```

lys-wm renders with Vulkan and stops on a card without a Vulkan driver, such as
a VM's virtio-gpu without venus. There, `greeter.renderer = "software"` runs the
greeter's lys-wm with GLES on llvmpipe; set
`environment.sessionVariables.LYS_WM_RENDERER = "gles"` instead to cover the
greeter and every lys-wm session, which inherit it through PAM.

With [lys-splash](https://github.com/stubbedev/lys-splash) as boot splash, set
`greeter.splashPackage`. The unit then conflicts with `lys-splash-quit.service`,
runs `lys-splash deactivate` before lys-wm starts, and the daemon runs
`lys-splash quit --retain-splash` once the greeter's first frame is presented,
so there is no blank frame.

## Lock screen

```nix
programs.lys-login = {
  enable = true;
  lock.enable = true;
};
```

`enable` (or `greeter.enable`) puts `lys-login` on the PATH. `lock.enable` adds
the [PAM service](/guide/pam#the-lock-screen-s-service). Set
`login.lock.pam-service` in your lys-shell config to `lock.pamService`.

## Under greetd

`greeter.displayManager = "greetd"` runs the greeter under greetd in a cage kiosk,
with state owned by greetd's `greeter` user.

## Options

All under `programs.lys-login`.

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `enable` | bool | `false` | `lys-login` on the PATH. |
| `package` | package | this flake's | The lys-login package. |
| `autologin.user` | null or string | `null` | Account logged in once per boot before the first greeter ([Autologin](/guide/pam#autologin)). Requires `displayManager = "lys-login"`. |
| `autologin.session` | null or string | `null` | Session id to start (a session file's name, `-x11` suffix for X11); null takes the last session, else the first. |
| `lock.enable` | bool | `false` | Declare the lock screen's PAM service. |
| `lock.pamService` | string | `"lys-shell"` | Its name. |
| `greeter.enable` | bool | `false` | Run the greeter via `greeter.displayManager`. |
| `greeter.displayManager` | `"lys-login"` or `"greetd"` | `"lys-login"` | `lys-login`: the daemon on `greeter.vt`. `greetd`: greetd with cage. |
| `greeter.compositorPackage` | package | lys-login's lys-wm input | lys-wm for the greeter kiosk. |
| `greeter.splashPackage` | null or package | `null` | lys-splash, when it is the boot splash. |
| `greeter.vt` | positive int | `1` | VT the daemon runs on. |
| `greeter.settings` | YAML | `{}` | Written to `/etc/lys-shell/config.yaml` when non-empty. |
| `greeter.session.dirs` | list of strings | NixOS `wayland-sessions` | Directories of Wayland session files. |
| `greeter.session.x11Dirs` | list of strings | NixOS `xsessions` | X11 session files, started through `startx` (needs it on the PATH, e.g. `services.xserver.enable`). `[]` disables X11. |
| `greeter.session.command` | string | `""` | Fallback session offered as "Custom"; the only entry when no session files exist. |
| `greeter.session.environment` | list of strings | `[]` | Extra `KEY=value` for every session. |
| `greeter.cagePackage` | package | `pkgs.cage` | greetd only: the kiosk compositor. |
| `greeter.renderer` | `"auto"` or `"software"` | `"auto"` | `software` needs no GPU driver: the greeter draws on the CPU, lys-wm renders with GLES (`LYS_WM_RENDERER=gles`) under the daemon, cage with pixman under greetd. Sessions are not affected. |
| `greeter.graphicsWrapper` | list of strings | `[]` | greetd only: command prefix around the launch, e.g. nixGL. |

Evaluation fails when no session can be offered, when `autologin.user` is set
under greetd, or when `graphicsWrapper` is set under the daemon.
