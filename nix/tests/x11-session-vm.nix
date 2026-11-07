# An X11 session through lys-login's display manager: the only session on
# offer comes from an xsessions directory, so the login runs it through
# `startx`, here a stand-in that records what the session was registered as
# instead of starting an X server. logind must hold it as Type=x11, while the
# greeter before it stays wayland.
{
  pkgs,
  self,
  lys-wm,
}: let
  user = "ada";
  password = "correct horse";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};
  xsessions = pkgs.writeTextDir "share/xsessions/stand-in.desktop" ''
    [Desktop Entry]
    Name=Stand-in
    Exec=stand-in-session
  '';
  startx = pkgs.writeShellScriptBin "startx" ''
    echo "$XDG_SESSION_TYPE $*" > /tmp/lys-login/session
    loginctl show-session "$XDG_SESSION_ID" -p Type > /tmp/lys-login/logind
    sleep 5
  '';
in
  pkgs.testers.nixosTest {
    name = "lys-login-x11-session-vm";

    nodes.machine = {...}: {
      imports = [
        self.nixosModules.default
        lys-wm.nixosModules.default
        greeterVm.module
      ];

      programs.lys-wm.enable = true;
      programs.lys-login.greeter = {
        enable = true;
        session = {
          dirs = [];
          x11Dirs = ["${xsessions}/share/xsessions"];
        };
      };

      environment.systemPackages = [startx];

      users.users.${user} = {
        isNormalUser = true;
        inherit password;
      };
    };

    testScript = ''
      ${greeterVm.script "-t lys-login"}

      start_all()
      machine.wait_for_unit("lys-login.service")
      machine.wait_until_succeeds("pgrep -u lys-greeter -f 'lys-login greet'", timeout=90)
      machine.wait_until_succeeds("loginctl list-sessions --no-legend | grep -q lys-greeter", timeout=30)
      sessions = machine.succeed("loginctl list-sessions --no-legend | awk '/lys-greeter/ {print $1}'").split()
      greeter = [machine.succeed(f"loginctl show-session {s} -p Class -p Type") for s in sessions]
      if not any("Class=greeter" in g and "Type=wayland" in g for g in greeter):
          raise Exception(f"no wayland greeter session among {greeter}")
      await_ready(machine, "username", 1)

      machine.send_chars("${user}\n${password}\n")
      try:
          machine.wait_until_succeeds("test -s /tmp/lys-login/logind", timeout=60)
      except Exception:
          machine.screenshot("timeout")
          print(machine.execute("journalctl -b --no-pager -n 200")[1])
          raise

      session = machine.succeed("cat /tmp/lys-login/session").split()
      if session[:3] != ["x11", "/usr/bin/env", "stand-in-session"]:
          raise Exception(f"the session ran as {session}")
      logind = machine.succeed("cat /tmp/lys-login/logind")
      if "Type=x11" not in logind:
          raise Exception(f"logind holds the X11 session as {logind}")
    '';
  }
