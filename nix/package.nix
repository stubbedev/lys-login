{
  self,
  inputs,
  ...
}: {
  perSystem = {
    lib,
    pkgs,
    system,
    ...
  }: let
    # libpam (authentication and sessions), libvulkan and libva: what
    # lys-shell's binaries dlopen through purego, the login screen
    # drawing through the same lys-ui.
    inherit (inputs.lys-shell.packages.${system}.lys-shell) libs;
  in {
    packages.default = pkgs.buildGo127Module {
      pname = "lys-login";
      version = "0-unstable-${self.lastModifiedDate or "0"}";

      src = lib.fileset.toSource {
        root = ../.;
        fileset = lib.fileset.difference (lib.fileset.gitTracked ../.) (
          lib.fileset.unions [
            ../flake.nix
            ../flake.lock
            ../nix
          ]
        );
      };

      # The module cache's hash; `just vendor-hash` recomputes it after a
      # go.mod change.
      vendorHash = "sha256-1JVIeCk4gPhtX5DK7h/6iprZmunOUa+K9V2Ct3qIuVU=";

      subPackages = ["cmd/lys-login"];

      env.CGO_ENABLED = 0;
      ldflags = ["-s" "-w"];

      # The suite runs in `just check`: it needs a compositor and D-Bus.
      doCheck = false;

      nativeBuildInputs = [pkgs.makeWrapper];

      # The polkit action for `pkexec lys-login apply-config`.
      postInstall = ''
        install -Dm0644 resources/dev.stubbe.lys.greeter.policy -t $out/share/polkit-1/actions
      '';

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
