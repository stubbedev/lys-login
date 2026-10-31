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
    daemonSystem = greeter:
      (pkgs.nixos {
        imports = [self.nixosModules.default];
        programs.lys-login.greeter =
          {
            enable = true;
            displayManager = "lys-login";
            compositorPackage = fakeLysWm;
          }
          // greeter;
        boot.loader.grub.enable = false;
        fileSystems."/" = {
          device = "nodev";
          fsType = "tmpfs";
        };
        system.stateVersion = lib.trivial.release;
      }).config;
    failedAssertions = greeter: map (a: a.message) (lib.filter (a: !a.assertion) (daemonSystem greeter).assertions);
  in {
    checks =
      lib.optionalAttrs pkgs.stdenv.hostPlatform.isx86_64 {
        # lys-login as the display manager in a NixOS VM (needs KVM); see
        # nix/tests/login-vm.nix.
        login-vm = pkgs.callPackage ./tests/login-vm.nix {
          inherit self;
          inherit (inputs) lys-wm lys-secrets;
        };
      }
      // {
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
          rejects = session: lib.any (lib.hasInfix "offers no session to log into") (failedAssertions {inherit session;});
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
        daemon-render-assertion = let
          rejects = greeter: needle: lib.any (lib.hasInfix needle) (failedAssertions greeter);
        in
          pkgs.runCommand "lys-login-daemon-render-assertion" {} ''
            ${lib.optionalString (!rejects {renderer = "software";} "greeter.renderer") ''echo "renderer = software passed evaluation under the lys-login display manager" >&2; exit 1''}
            ${lib.optionalString (!rejects {graphicsWrapper = ["nixGL"];} "greeter.graphicsWrapper") ''echo "a graphicsWrapper passed evaluation under the lys-login display manager" >&2; exit 1''}
            ${lib.optionalString (rejects {} "greeter.renderer" || rejects {} "greeter.graphicsWrapper") ''echo "the default renderer and graphicsWrapper were rejected" >&2; exit 1''}
            touch $out
          '';
        # The package wraps the binary with the libraries it dlopens, libpam
        # above all.
        package = pkgs.runCommand "lys-login-package" {} ''
          grep -q "${lib.getLib pkgs.pam}/lib" ${pkg}/bin/lys-login || { echo "lys-login lacks libpam on its library path" >&2; exit 1; }
          touch $out
        '';
      };
  };
}
