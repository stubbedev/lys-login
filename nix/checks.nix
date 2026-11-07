{
  self,
  inputs,
  ...
}: {
  perSystem = {
    lib,
    pkgs,
    ...
  }: let
    pkg = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    fakeLysWm = pkgs.writeShellScriptBin "lys-wm" "";
    greeterSystem = greeter:
      (pkgs.nixos {
        imports = [self.nixosModules.default];
        programs.lys-login.greeter = {enable = true;} // greeter;
        boot.loader.grub.enable = false;
        fileSystems."/" = {
          device = "nodev";
          fsType = "tmpfs";
        };
        system.stateVersion = lib.trivial.release;
      }).config;
    daemonSystem = greeter:
      greeterSystem ({
          displayManager = "lys-login";
          compositorPackage = fakeLysWm;
        }
        // greeter);
    failedAssertions = system: map (a: a.message) (lib.filter (a: !a.assertion) system.assertions);
  in {
    checks =
      lib.optionalAttrs pkgs.stdenv.hostPlatform.isx86_64 {
        # lys-login as the display manager in a NixOS VM (needs KVM); see
        # nix/tests/login-vm.nix.
        login-vm = pkgs.callPackage ./tests/login-vm.nix {
          inherit self;
          inherit (inputs) lys-wm lys-secrets;
        };
        # The login keyring with the real gnome-keyring; see nix/tests/keyring-vm.nix.
        keyring-vm = pkgs.callPackage ./tests/keyring-vm.nix {
          inherit self;
          inherit (inputs) lys-wm;
        };
        upgrade-vm = pkgs.callPackage ./tests/upgrade-vm.nix {
          inherit self;
          inherit (inputs) lys-wm lys-secrets;
        };
        # The greeter under greetd in cage; see nix/tests/greetd-vm.nix.
        greetd-vm = pkgs.callPackage ./tests/greetd-vm.nix {inherit self;};
        # Setting the wall clock wakes the login screen's clock; see
        # nix/tests/wallclock-vm.nix.
        wallclock-vm = pkgs.callPackage ./tests/wallclock-vm.nix {inherit pkg;};
        # The greeter types with the system keyboard layout and switches between
        # several; see nix/tests/keyboard-vm.nix.
        keyboard-vm = pkgs.callPackage ./tests/keyboard-vm.nix {
          inherit self;
          inherit (inputs) lys-wm;
        };
        # The greeter's accessibility menu; see nix/tests/accessibility-vm.nix.
        accessibility-vm = pkgs.callPackage ./tests/accessibility-vm.nix {
          inherit self;
          inherit (inputs) lys-wm;
        };
        # The lock screen's status row and fingerprint; see nix/tests/lock-vm.nix.
        lock-vm = pkgs.callPackage ./tests/lock-vm.nix {
          inherit self;
          inherit (inputs) lys-wm;
        };
        # An X11 session reaches logind as x11; see nix/tests/x11-session-vm.nix.
        x11-session-vm = pkgs.callPackage ./tests/x11-session-vm.nix {
          inherit self;
          inherit (inputs) lys-wm;
        };
      }
      // {
        upgrade-unit = let
          unit = (daemonSystem {session.environment = ["A=b c"];}).systemd.services.lys-login;
          daemonArgs = lib.last (lib.splitString " daemon " unit.serviceConfig.ExecStart);
        in
          pkgs.runCommand "lys-login-upgrade-unit" {} ''
            ${lib.optionalString (!unit.reloadIfChanged) ''echo "a switch does not reload the changed unit" >&2; exit 1''}
            ${lib.optionalString unit.restartIfChanged ''echo "a switch may restart the display manager under a session" >&2; exit 1''}
            grep -qF -- ${lib.escapeShellArg " upgrade -- ${daemonArgs} "} ${unit.serviceConfig.ExecReload} || { echo "ExecReload upgrades to other arguments than ExecStart runs with" >&2; exit 1; }
            touch $out
          '';
        splash-unit = let
          splash = pkgs.writeShellScriptBin "lys-splash" "";
          unit = splashPackage: (daemonSystem {inherit splashPackage;}).systemd.services.lys-login;
          handsOver = u:
            lib.elem "lys-splash-quit.service" u.conflicts
            && lib.all (s: lib.elem s u.after) ["lys-splash-start.service" "lys-splash-quit.service"]
            && (u.serviceConfig.ExecStartPre or null) == "-${lib.getExe splash} deactivate"
            && lib.hasInfix (builtins.unsafeDiscardStringContext "--splash ${lib.getExe splash} ") (builtins.unsafeDiscardStringContext u.serviceConfig.ExecStart);
          touchesSplash = u:
            lib.any (s: lib.hasInfix "lys-splash" (builtins.unsafeDiscardStringContext s)) (u.conflicts ++ u.after ++ [u.serviceConfig.ExecStart (u.serviceConfig.ExecStartPre or "")]);
        in
          pkgs.runCommand "lys-login-splash-unit" {} ''
            ${lib.optionalString (!handsOver (unit splash)) ''echo "with splashPackage the unit does not take the screen from lys-splash" >&2; exit 1''}
            ${lib.optionalString (touchesSplash (unit null)) ''echo "without splashPackage the unit names lys-splash" >&2; exit 1''}
            touch $out
          '';
        greeter-sessions-assertion = let
          rejects = session: lib.any (lib.hasInfix "offers no session to log into") (failedAssertions (daemonSystem {inherit session;}));
        in
          pkgs.runCommand "lys-login-greeter-sessions-assertion" {} ''
            ${lib.optionalString (!rejects {
              dirs = [];
              x11Dirs = [];
            }) ''echo "no session dirs and no session.command passed evaluation" >&2; exit 1''}
            ${lib.optionalString (rejects {}) ''echo "the default session dirs were rejected" >&2; exit 1''}
            ${lib.optionalString (rejects {
              dirs = [];
              x11Dirs = [];
              command = "niri --session";
            }) ''echo "session.command alone was rejected" >&2; exit 1''}
            ${lib.optionalString (rejects {
              dirs = [];
              x11Dirs = ["/x"];
            }) ''echo "x11Dirs alone was rejected" >&2; exit 1''}
            touch $out
          '';
        greetd-software-render = let
          command = renderer:
            (greeterSystem {
              displayManager = "greetd";
              inherit renderer;
            }).services.greetd.settings.default_session.command;
          has = needle: renderer: lib.hasInfix needle (builtins.unsafeDiscardStringContext (command renderer));
        in
          pkgs.runCommand "lys-login-greetd-software-render" {} ''
            ${lib.optionalString (!(has "LYS_UI_RENDERER=cpu" "software") || !(has "WLR_RENDERER=pixman" "software")) ''echo "renderer = software leaves cage or the greeter on the GPU" >&2; exit 1''}
            ${lib.optionalString (has "LYS_UI_RENDERER" "auto" || has "WLR_RENDERER" "auto") ''echo "renderer = auto forces a software path" >&2; exit 1''}
            touch $out
          '';
        daemon-render-assertion = let
          rejects = greeter: needle: lib.any (lib.hasInfix needle) (failedAssertions (daemonSystem greeter));
        in
          pkgs.runCommand "lys-login-daemon-render-assertion" {} ''
            ${lib.optionalString (!rejects {renderer = "software";} "greeter.renderer") ''echo "renderer = software passed evaluation under the lys-login display manager" >&2; exit 1''}
            ${lib.optionalString (!rejects {graphicsWrapper = ["nixGL"];} "greeter.graphicsWrapper") ''echo "a graphicsWrapper passed evaluation under the lys-login display manager" >&2; exit 1''}
            ${lib.optionalString (rejects {} "greeter.renderer" || rejects {} "greeter.graphicsWrapper") ''echo "the default renderer and graphicsWrapper were rejected" >&2; exit 1''}
            touch $out
          '';
        default-display-manager = let
          system = greeterSystem {};
          greetd = greeterSystem {displayManager = "greetd";};
          runsDaemonIn = compositor: sys:
            lib.hasInfix
            (builtins.unsafeDiscardStringContext " daemon --vt 1 --service lys-login --greeter-user lys-greeter --compositor ${lib.getExe' compositor "lys-wm"} ")
            (builtins.unsafeDiscardStringContext sys.systemd.services.lys-login.serviceConfig.ExecStart);
          defaultCompositor = inputs.lys-wm.packages.${pkgs.stdenv.hostPlatform.system}.default;
        in
          pkgs.runCommand "lys-login-default-display-manager" {} ''
            ${lib.optionalString (failedAssertions system != []) ''echo "greeter.enable alone fails an assertion" >&2; exit 1''}
            ${lib.optionalString (!(system.systemd.services ? lys-login) || !(runsDaemonIn defaultCompositor system)) ''echo "greeter.enable alone does not run the lys-login daemon in the flake's lys-wm" >&2; exit 1''}
            ${lib.optionalString system.services.greetd.enable ''echo "greeter.enable alone enabled greetd" >&2; exit 1''}
            ${lib.optionalString (!(runsDaemonIn fakeLysWm (daemonSystem {}))) ''echo "compositorPackage does not reach the daemon" >&2; exit 1''}
            ${lib.optionalString (!greetd.services.greetd.enable || greetd.systemd.services ? lys-login) ''echo "displayManager = greetd does not run greetd instead of the daemon" >&2; exit 1''}
            touch $out
          '';
        # The package wraps the binary with the libraries it dlopens, libpam
        # above all.
        package = pkgs.runCommand "lys-login-package" {} ''
          grep -q "${lib.getLib pkgs.pam}/lib" ${pkg}/bin/lys-login || { echo "lys-login lacks libpam on its library path" >&2; exit 1; }
          grep -q "${pkgs.xkeyboard-config}/share/X11/xkb" ${pkg}/bin/.lys-login-wrapped || { echo "lys-login lacks the keyboard data path" >&2; exit 1; }
          grep -q "${pkgs.libX11}/share/X11/locale" ${pkg}/bin/.lys-login-wrapped || { echo "lys-login lacks the locale data path" >&2; exit 1; }
          touch $out
        '';
      };
  };
}
