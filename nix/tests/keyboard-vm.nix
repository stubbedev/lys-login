# The greeter types with the system's keyboard layout under lys-login's display
# manager, set the normal NixOS way (services.xserver.xkb, which
# systemd-localed reads), with no XKB_DEFAULT_* anywhere.
#
# On a German machine the password "zürichy" logs in only when typed on the
# German layout: the keys QEMU sends are US keys, so the script sends what
# German puts on them, "y[richz".
#
# On a machine with German and US layouts and Caps Lock switching between
# them, the greeter starts on German, Caps Lock moves the password field to
# US, and a US password logs in. The greeter remembers that the user typed in
# US, and the next greeter switches to US on its own for the remembered user.
#
# Build: nix build .#checks.<system>.keyboard-vm -L
{
  pkgs,
  lib,
  self,
  lys-wm,
}: let
  user = "alice";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};

  # NixOS hashes `password` with Perl's crypt, which takes "ü" as one Latin-1
  # byte: the hash comes from mkpasswd, over the UTF-8 bytes a login sends.
  hashed = password:
    toString (pkgs.runCommand "lys-login-keyboard-vm-password" {nativeBuildInputs = [pkgs.mkpasswd];} ''
      mkpasswd -m sha-512 -S lyskeyboardvmslt ${lib.escapeShellArg password} > $out
    '');

  session = pkgs.writeShellScript "lys-login-keyboard-vm-session" ''
    id -un >> /tmp/lys-login/sessions
    sleep 3
  '';

  node = {
    xkb,
    password,
  }: {
    imports = [
      self.nixosModules.default
      lys-wm.nixosModules.default
      greeterVm.module
    ];
    programs.lys-wm.enable = true;
    services.xserver.xkb = xkb;
    programs.lys-login.greeter = {
      enable = true;
      session = {
        dirs = [];
        x11Dirs = [];
        command = "${session}";
      };
    };
    users.users.${user} = {
      isNormalUser = true;
      hashedPasswordFile = hashed password;
    };
  };
in
  pkgs.testers.nixosTest {
    name = "lys-login-keyboard-vm";

    nodes.german = node {
      xkb.layout = "de";
      password = "zürichy";
    };
    nodes.switching = node {
      xkb = {
        layout = "de,us";
        options = "grp:caps_toggle";
      };
      password = "zy-pass";
    };

    testScript = ''
      ${greeterVm.script "-t lys-login"}

      def journal(machine):
          return machine.succeed("journalctl -t lys-login -o cat --no-pager")

      def await_layout(machine, name, count):
          machine.wait_until_succeeds(
              f"[ \"$(journalctl -t lys-login -o cat --no-pager | grep -c 'greeter: keyboard layout \"{name}\"')\" -ge {count} ]",
              timeout=60,
          )

      def sessions(machine):
          return machine.succeed("cat /tmp/lys-login/sessions 2>/dev/null || true").split()

      def await_sessions(machine, count):
          machine.wait_until_succeeds(f"[ \"$(wc -l < /tmp/lys-login/sessions)\" -ge {count} ]", timeout=60)

      def system_layout_only(machine, layout):
          machine.wait_for_unit("lys-login.service")
          machine.wait_until_succeeds("pgrep -u lys-greeter -f 'lys-login greet'", timeout=90)
          localed = machine.succeed("localectl status")
          if f"X11 Layout: {layout}" not in localed:
              raise Exception(f"localed does not report {layout}:\n{localed}")
          kiosk = machine.succeed("cat /run/lys-login/kiosk.yaml")
          if f'layout: "{layout}"' not in kiosk:
              raise Exception(f"the kiosk config lacks the layout {layout}:\n{kiosk}")
          for proc in ("lys-login daemon", "lys-login greet"):
              pid = machine.succeed(f"pgrep -f '{proc}' | head -n 1").strip()
              env = machine.succeed(f"tr '\\0' '\\n' < /proc/{pid}/environ")
              if "XKB_DEFAULT" in env:
                  raise Exception(f"{proc} runs with XKB_DEFAULT_*:\n{env}")

      german.start()
      system_layout_only(german, "de")
      await_ready(german, "username", 1)
      await_layout(german, "German", 1)
      german.screenshot("german-01-greeter")
      german.send_chars("${user}\ny[richz\n")
      await_sessions(german, 1)
      if sessions(german) != ["${user}"]:
          raise Exception(f"the German password started {sessions(german)}:\n" + journal(german))
      german.shutdown()

      switching.start()
      system_layout_only(switching, "de,us")
      await_ready(switching, "username", 1)
      await_layout(switching, "German", 1)
      switching.screenshot("switching-01-greeter-german")
      switching.send_key("caps_lock")
      await_layout(switching, "English (US)", 1)
      switching.screenshot("switching-02-greeter-us")
      switching.send_chars("${user}\nzy-pass\n")
      await_sessions(switching, 1)
      remembered = switching.succeed("cat /var/lib/lys-greeter/last-layout/${user}").strip()
      if remembered != "English (US)":
          raise Exception(f"the greeter remembered {remembered!r} for ${user}")

      await_ready(switching, "password", 1)
      await_layout(switching, "English (US)", 2)
      switching.screenshot("switching-03-greeter-remembered-us")
      switching.send_chars("zy-pass\n")
      await_sessions(switching, 2)
      if sessions(switching) != ["${user}", "${user}"]:
          raise Exception(f"the logins started {sessions(switching)}:\n" + journal(switching))
      print(journal(switching)[-4000:])
    '';
  }
