{inputs, ...}: {
  perSystem = {
    lib,
    pkgs,
    self',
    system,
    ...
  }: let
    shell = inputs.lys-desktop.devShells.${system}.default;
    gpuDrivers = inputs.lys-desktop.inputs.lys-lib.lib.gpuDriverEnv pkgs;
  in {
    # lys-desktop's dev shell: the lint stack, fonts and the headless sway
    # and private D-Bus the tests start, with its fontconfig and GPU
    # driver setup (its shellHook).
    devShells.default = pkgs.mkShell {
      inputsFrom = [self'.packages.default shell];

      packages = ["${inputs.lys-ci}"];

      LD_LIBRARY_PATH = lib.makeLibraryPath (lib.unique (gpuDrivers.libraries ++ self'.packages.default.libs));
      CGO_ENABLED = "0";
      GOTOOLCHAIN = "local";
    };

    formatter = pkgs.alejandra;
  };
}
