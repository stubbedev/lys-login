{
  description = "lys-login - the lys desktop's display manager, greeter and lock screen";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    lys-ci.url = "github:stubbedev/lys-ci";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
    # The dev shell's test stack and the GPU driver setup through its
    # lys-lib input. The Go code comes through go.mod.
    lys-desktop = {
      url = "github:stubbedev/lys-desktop";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-parts.follows = "flake-parts";
      inputs.lys-ci.follows = "lys-ci";
      inputs.lys-wm.follows = "lys-wm";
    };
    # The Secret Service the login VM check finds unlocked by the login.
    lys-secrets = {
      url = "github:stubbedev/lys-secrets";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-parts.follows = "flake-parts";
    };
    # The kiosk compositor the display manager shows the greeter in: the
    # NixOS module's default greeter.compositorPackage, and the login VM
    # check's.
    lys-wm = {
      url = "github:stubbedev/lys-wm";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-parts.follows = "flake-parts";
      inputs.lys-shell.follows = "";
    };
  };

  outputs = inputs @ {
    flake-parts,
    nixpkgs,
    ...
  }:
    flake-parts.lib.mkFlake {inherit inputs;} {
      systems = ["x86_64-linux" "aarch64-linux"];
      imports = [
        ./nix/package.nix
        ./nix/shell.nix
        ./nix/modules.nix
        ./nix/checks.nix
        ./nix/distro-vm.nix
      ];
    };
}
