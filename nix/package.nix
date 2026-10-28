{self, ...}: {
  perSystem = {
    lib,
    pkgs,
    ...
  }: {
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

      vendorHash = null;

      env.CGO_ENABLED = 0;

      doCheck = true;

      meta = {
        description = "the lys desktop's display manager, greeter and lock screen";
        homepage = "https://github.com/stubbedev/lys-login";
        license = lib.licenses.mit;
        mainProgram = "lys-login";
        platforms = lib.platforms.unix;
      };
    };
  };
}
