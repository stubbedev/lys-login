{self, ...}: {
  flake = {
    # NixOS: `imports = [lys-login.nixosModules.default]; programs.lys-login.greeter.enable = true;`
    nixosModules.default = import ./nixos-module.nix self;

    # home-manager: `imports = [lys-login.homeManagerModules.default];`
    homeManagerModules.default = import ./hm-module.nix self;
  };
}
