# The clock on the login screen against the kernel: setting the wall clock wakes
# its timer at once, which takes CAP_SYS_TIME and moves the machine's time, so it
# runs in a VM and nowhere else.
#
# Build: nix build .#checks.<system>.wallclock-vm -L
{
  pkgs,
  pkg,
}: let
  screenTest = pkg.overrideAttrs {
    pname = "lys-login-screen-test";
    postFixup = "";
    buildPhase = ''
      runHook preBuild
      go test -c -o screen.test ./internal/screen
      runHook postBuild
    '';
    doCheck = false;
    installPhase = ''
      runHook preInstall
      install -Dm755 screen.test $out/bin/screen.test
      runHook postInstall
    '';
  };
in
  pkgs.testers.runNixOSTest {
    name = "lys-login-wallclock";
    nodes.machine = {
      virtualisation.graphics = false;
      services.timesyncd.enable = false;
    };
    testScript = ''
      machine.wait_for_unit("multi-user.target")
      machine.succeed(
          "cd /tmp && LYS_LOGIN_WALLCLOCK_VM=1 ${screenTest}/bin/screen.test "
          + "-test.v -test.count=1 -test.run '^TestSettingTheClockWakesTheTimer$' >&2"
      )
    '';
  }
