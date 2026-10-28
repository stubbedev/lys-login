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
        # The package wraps the binary with the libraries it dlopens (libpam
        # above all) and installs the polkit action apply-config runs under.
        package = pkgs.runCommand "lys-login-package" {} ''
          grep -q "${lib.getLib pkgs.pam}/lib" ${pkg}/bin/lys-login || { echo "lys-login lacks libpam on its library path" >&2; exit 1; }
          test -f ${pkg}/share/polkit-1/actions/dev.stubbe.lys.greeter.policy
          touch $out
        '';
      };
  };
}
