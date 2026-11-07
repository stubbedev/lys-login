# The greeter's accessibility menu under lys-login's display manager in a NixOS
# VM. With every option off the greeter starts no accessibility process. Super+
# Alt+S starts the screen reader, Orca from a wrapper that keeps its debug log,
# and Orca speaks the name of the field the greeter focuses, which it reads over
# AT-SPI from the greeter. The choice is remembered: the next greeter starts with
# the reader again, and with the large text and high contrast set beside it.
#
# Build: nix build .#checks.<system>.accessibility-vm -L
{
  pkgs,
  self,
  lys-wm,
}: let
  user = "alice";
  password = "lys-a11y-vm";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};

  orca = pkgs.writeShellScript "lys-login-vm-orca" ''
    exec ${pkgs.orca}/bin/orca --debug-file=/tmp/lys-login/orca.log "$@"
  '';

  orcaLineBuffered = pkgs.writeText "orca-customizations.py" ''
    from orca import debug

    debug.debugFile.reconfigure(line_buffering=True)
  '';

  session = pkgs.writeShellScript "lys-login-a11y-vm-session" ''
    id -un >> /tmp/lys-login/sessions
    sleep 2
  '';
in
  pkgs.testers.nixosTest {
    name = "lys-login-accessibility-vm";
    globalTimeout = 600;

    nodes.machine = {...}: {
      imports = [
        self.nixosModules.default
        lys-wm.nixosModules.default
        greeterVm.module
      ];
      programs.lys-wm.enable = true;
      programs.lys-login.greeter = {
        enable = true;
        settings.login.greeter.screen-reader = "${orca}";
        session = {
          dirs = [];
          x11Dirs = [];
          command = "${session}";
        };
      };
      systemd.tmpfiles.rules = [
        "d /var/lib/lys-greeter/.local 0700 lys-greeter lys-greeter -"
        "d /var/lib/lys-greeter/.local/share 0700 lys-greeter lys-greeter -"
        "d /var/lib/lys-greeter/.local/share/orca 0700 lys-greeter lys-greeter -"
        "L+ /var/lib/lys-greeter/.local/share/orca/orca-customizations.py - - - - ${orcaLineBuffered}"
      ];
      users.users.${user} = {
        isNormalUser = true;
        inherit password;
      };
    };

    testScript = ''
      import time

      ${greeterVm.script "-t lys-login"}

      A11Y = "at-spi-bus-launcher|at-spi2-registryd|orca"

      def a11y_processes():
          return machine.succeed(f"pgrep -a -u lys-greeter -f '{A11Y}' || true").strip()

      def diagnose(what):
          machine.screenshot(f"failed-{what}")
          print(machine.succeed("ps -u lys-greeter -o pid,args || true"))
          print(machine.succeed("journalctl -b --no-pager -n 300 || true"))
          print(machine.succeed("grep -n -i -E 'greeter|focus|SPEECH|application|registry|desktop' /tmp/lys-login/orca.log | head -n 300 || true"))
          uid = machine.succeed("id -u lys-greeter").strip()
          print(machine.execute(f"su lys-greeter -s /bin/sh -c 'XDG_RUNTIME_DIR=/run/user/{uid} ${pkgs.orca}/bin/orca --list-apps' 2>&1")[1])

      def until(cmd, what, timeout=60):
          try:
              machine.wait_until_succeeds(cmd, timeout=timeout)
          except Exception:
              diagnose(what)
              raise

      def journal():
          return machine.succeed("journalctl -t lys-login -o cat --no-pager")

      def await_journal(needle, count, what):
          end = time.monotonic() + 60
          while journal().count(needle) < count:
              if time.monotonic() >= end:
                  machine.screenshot(f"timeout-{what}")
                  raise Exception(f"no {what} ({needle!r} x{count}) in the journal:\n" + journal()[-4000:])
              time.sleep(0.5)

      def orca_spoke(name):
          return machine.execute(f"grep -q \"SPEECH OUTPUT: '.*{name}\" /tmp/lys-login/orca.log")[0] == 0

      machine.start()
      machine.wait_for_unit("lys-login.service")
      await_ready(machine, "username", 1)
      machine.screenshot("01-greeter")
      running = a11y_processes()
      if running:
          raise Exception(f"with every option off the greeter runs {running}")
      machine.fail("test -e /var/lib/lys-greeter/accessibility")

      machine.send_key("meta_l-alt-s")
      await_journal("greeter: accessibility: screen-reader on", 1, "the screen reader turned on")
      await_journal("greeter: accessibility: screen reader", 1, "the screen reader started")
      until("pgrep -u lys-greeter -f orca", "orca")
      until("pgrep -u lys-greeter -f at-spi-bus-launcher", "the AT-SPI bus")
      until("grep -q \"SPEECH OUTPUT: 'Screen reader on.'\" /tmp/lys-login/orca.log", "Orca ready", timeout=120)
      time.sleep(2)
      end = time.monotonic() + 60
      while not orca_spoke("Username"):
          if time.monotonic() >= end:
              diagnose("speech")
              raise Exception("Orca never spoke the focused field's name")
          machine.send_key("tab")
          time.sleep(1)
          machine.send_key("shift-tab")
          time.sleep(2)
      machine.screenshot("02-screen-reader")
      remembered = machine.succeed("cat /var/lib/lys-greeter/accessibility").split()
      if remembered != ["screen-reader"]:
          raise Exception(f"the greeter remembered {remembered}")
      machine.send_key("meta_l-alt-s")
      await_journal("greeter: accessibility: screen-reader off", 1, "the screen reader turned off")
      until("! pgrep -u lys-greeter -x orca", "Orca's exit")

      machine.succeed("printf 'screen-reader\\nlarge-text\\nhigh-contrast\\n' > /var/lib/lys-greeter/accessibility")
      machine.succeed("chown lys-greeter:lys-greeter /var/lib/lys-greeter/accessibility")
      machine.send_chars("${user}\n${password}\n")
      machine.wait_until_succeeds("grep -qx ${user} /tmp/lys-login/sessions", timeout=60)
      await_ready(machine, "password", 1)
      for want in ("screen-reader on", "large-text on", "high-contrast on"):
          await_journal(f"greeter: accessibility: {want}", 2 if want == "screen-reader on" else 1, want)
      machine.wait_until_succeeds("pgrep -u lys-greeter -f orca", timeout=60)
      machine.screenshot("03-next-greeter-large-high-contrast")
      print(journal()[-3000:])
    '';
  }
