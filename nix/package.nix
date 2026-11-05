{inputs, ...}: {
  perSystem = {
    lib,
    pkgs,
    system,
    ...
  }: let
    src = lib.fileset.toSource {
      root = ../.;
      fileset = lib.fileset.difference (lib.fileset.gitTracked ../.) (
        lib.fileset.unions [
          ../flake.nix
          ../flake.lock
          ../nix
          ../docs
        ]
      );
    };
    # What the binary dlopens through purego: libpam for authentication
    # and sessions, and what lys-shell's binaries load for drawing through
    # the same lys-ui (libvulkan, libva).
    libs = lib.unique ([pkgs.pam] ++ inputs.lys-shell.packages.${system}.lys-shell.libs);
  in {
    packages.default = pkgs.buildGo127Module {
      pname = "lys-login";
      version = "0-unstable-${lib.substring 0 8 (builtins.unsafeDiscardStringContext (baseNameOf src))}";

      inherit src;

      # The module cache's hash; `just vendor-hash` recomputes it after a
      # go.mod change.
      vendorHash = "sha256-KLg/YdSG+BBhOWBERoxAmbraM4Ovld5SdO0rDc9e6tY=";

      subPackages = ["cmd/lys-login"];

      env.CGO_ENABLED = 0;
      ldflags = [
        "-s"
        "-w"
        "-X github.com/stubbedev/lys-lib/xkb.defaultRoot=${pkgs.xkeyboard-config}/share/X11/xkb"
        "-X github.com/stubbedev/lys-lib/xkb.defaultXLocaleDir=${pkgs.libX11}/share/X11/locale"
      ];

      # The suite runs in `just check`: it needs a compositor and D-Bus.
      doCheck = false;

      nativeBuildInputs = [pkgs.makeWrapper];

      postFixup = ''
        wrapProgram $out/bin/lys-login --prefix LD_LIBRARY_PATH : "${lib.makeLibraryPath libs}"
      '';

      passthru = {inherit libs;};

      meta = {
        description = "the lys desktop's display manager, greeter and lock screen";
        homepage = "https://github.com/stubbedev/lys-login";
        license = lib.licenses.mit;
        mainProgram = "lys-login";
        platforms = lib.platforms.linux;
      };
    };
  };
}
