# The release's .deb and .rpm, built and installed in a Debian and a
# Fedora cloud VM the way CI builds them, then logged in through: see
# scripts/distro-vm. lys-secrets' package is built and installed beside
# it from the flake's lys-secrets, whose static binary runs on either
# distribution, so the login can unlock its login collection. Run with
# `just distro-vm <debian|fedora>`; it needs KVM and the guest needs the
# network for the build tools.
{
  lib,
  inputs,
  ...
}: {
  perSystem = {pkgs, ...}: let
    images = {
      debian = pkgs.fetchurl {
        url = "https://cloud.debian.org/images/cloud/trixie/20261026-2643/debian-13-generic-amd64-20261026-2643.qcow2";
        sha512 = "5bd891c6e62d01639f52a5587b72a4607421e445732214be787dba6043cf9797a6ff90bed982aada6a6c7e619f1f956d961c4fad76757171abd607920aac098a";
      };
      fedora = pkgs.fetchurl {
        url = "https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-44-1.7.x86_64.qcow2";
        sha256 = "28680fe5b371a5a82ebf43a31926e086a168e59949d03969c5093e7071f90b7f";
      };
    };
    lysSecrets = inputs.lys-secrets.packages.${pkgs.stdenv.hostPlatform.system}.default;
    guest = pkgs.runCommand "lys-login-distro-vm-guest" {} ''
      cp -r ${lib.fileset.toSource {
        root = ../.;
        fileset = lib.fileset.unions [../packaging ../scripts/ci ../scripts/distro-vm];
      }} $out
      chmod -R u+w $out
      mkdir -p $out/lys-secrets/bin
      cp -r ${inputs.lys-secrets}/{LICENSE,packaging,resources,scripts} $out/lys-secrets/
      cp ${lysSecrets}/bin/lys-secrets $out/lys-secrets/bin/
    '';
    run = distro: image:
      pkgs.writeShellApplication {
        name = "lys-login-distro-vm-${distro}";
        runtimeInputs = [pkgs.qemu_kvm pkgs.xorriso];
        runtimeEnv = {
          DISTRO_VM_IMAGE = image;
          DISTRO_VM_GUEST = guest;
        };
        text = builtins.readFile ../scripts/distro-vm/run.sh;
      };
  in
    lib.mkIf pkgs.stdenv.hostPlatform.isx86_64 {
      packages = lib.mapAttrs' (distro: image: lib.nameValuePair "distro-vm-${distro}" (run distro image)) images;

      # The guest's scripts pass shellcheck, as run.sh does when the
      # packages above are built.
      checks.distro-vm-scripts = pkgs.runCommand "lys-login-distro-vm-scripts" {nativeBuildInputs = [pkgs.shellcheck pkgs.python3];} ''
        shellcheck ${../scripts/distro-vm}/guest.sh ${../scripts/distro-vm}/session.sh ${../scripts/distro-vm}/run.sh
        python3 -c 'import ast, sys; ast.parse(open(sys.argv[1]).read())' ${../scripts/distro-vm}/greeter.py
        touch $out
      '';
    };
}
