{
  description = "lys-login - the lys desktop's display manager, greeter and lock screen";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
    # The shell whose look and config the login screen shares: the
    # libraries its binaries dlopen, its dev shell's test stack, and the
    # GPU driver setup through its lys-lib input. The Go code comes
    # through go.mod.
    lys-shell = {
      url = "github:stubbedev/lys-shell";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-parts.follows = "flake-parts";
      inputs.lys-wm.follows = "lys-wm";
    };
    # The kiosk compositor the display manager shows the greeter in, for
    # the login VM check.
    lys-wm = {
      url = "github:stubbedev/lys-wm";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-parts.follows = "flake-parts";
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
      ];
    };
}
