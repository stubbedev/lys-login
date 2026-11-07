# Fast user switching under lys-login's display manager in a NixOS VM. alice
# logs in on VT 1 and locks; `lys-login switch-user`, which the lock screen's
# Switch user button runs too, shows a greeter on a free VT, where bob logs in.
# From bob's session the next greeter switches back to alice without asking her
# password: no session worker starts for her, and her lock screen is what shows.
# An upgrade with both sessions running keeps both. bob logging out returns to
# alice's VT, not to a greeter, and stopping the display manager ends every
# session.
#
# Build: nix build .#checks.<system>.switch-vm -L
{
  pkgs,
  self,
  lys-wm,
}: let
  password = "lys-switch-vm";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};

  requestUpgrade =
    pkgs.writers.writePython3 "lys-login-switch-vm-request-upgrade" {} ''
      import json
      import socket
      import sys

      conn = socket.socket(socket.AF_UNIX)
      conn.settimeout(60)
      conn.connect("/run/lys-login/upgrade.sock")
      request = {"exec": sys.argv[1], "args": sys.argv[2:]}
      conn.sendall(json.dumps(request).encode())
      conn.shutdown(socket.SHUT_WR)
      print(json.load(conn.makefile()))
    '';

  lockConfig = pkgs.writeText "lys-login-switch-vm-config.yaml" ''
    login:
      lock:
        pam-service: lys-shell
  '';

  sessionAgent = pkgs.writeShellScript "lys-login-switch-vm-agent" ''
    me=$(id -un)
    echo "$XDG_SESSION_ID $XDG_VTNR" > /tmp/lys-login/$me-session
    until [ -e /tmp/lys-login/stop-agents ]; do
      if [ -e /tmp/lys-login/$me-lock ]; then
        rm /tmp/lys-login/$me-lock
        lys-login lock > /tmp/lys-login/$me-lock.out 2> /tmp/lys-login/$me-lock.err &
      fi
      sleep 0.1
    done
  '';

  sessionConfig = pkgs.writeText "lys-login-switch-vm-session.yaml" ''
    binds: []
    animations:
      enabled: false
    autostart:
      - "${sessionAgent}"
  '';

  session = pkgs.writeShellScript "lys-login-switch-vm-session" ''
    exec lys-wm --config ${sessionConfig}
  '';
in
  pkgs.testers.runNixOSTest ({nodes, ...}: {
    name = "lys-login-switch-vm";
    globalTimeout = 600;

    nodes.machine = {...}: {
      imports = [
        self.nixosModules.default
        lys-wm.nixosModules.default
        greeterVm.module
      ];
      programs.lys-wm.enable = true;
      programs.lys-login = {
        lock.enable = true;
        greeter = {
          enable = true;
          session = {
            dirs = [];
            x11Dirs = [];
            command = "${session}";
          };
        };
      };
      users.users.alice = {
        isNormalUser = true;
        inherit password;
      };
      users.users.bob = {
        isNormalUser = true;
        inherit password;
      };
    };

    testScript = ''
      import time

      ${greeterVm.script "-t lys-login"}

      UPGRADE = "${nodes.machine.systemd.services.lys-login.serviceConfig.ExecReload}"

      def journal():
          return machine.succeed("journalctl -b -u lys-login -o cat --no-pager")

      def active_vt():
          return int(machine.succeed("cat /sys/class/tty/tty0/active").strip().removeprefix("tty"))

      def await_vt(vt, what):
          end = time.monotonic() + 60
          while active_vt() != vt:
              if time.monotonic() >= end:
                  raise Exception(f"{what}: VT {active_vt()} shows, want {vt}")
              time.sleep(0.3)

      def session_of(user):
          machine.wait_until_succeeds(f"test -s /tmp/lys-login/{user}-session", timeout=90)
          sid, vt = machine.succeed(f"cat /tmp/lys-login/{user}-session").split()
          return sid, int(vt)

      def workers():
          return sorted(int(p) for p in machine.succeed("pgrep -f '^/nix/store/[^ ]*/lys-login worker session' || true").split())

      def greeters():
          return machine.execute("pgrep -u lys-greeter -f 'lys-login greet'")[0] == 0

      def as_user(user, command):
          uid = machine.succeed(f"id -u {user}").strip()
          return f"su {user} -s /bin/sh -c 'XDG_RUNTIME_DIR=/run/user/{uid} {command}'"

      def switch_user(user, greeters):
          machine.succeed("rm -f /var/lib/lys-greeter/last-user")
          machine.succeed(as_user(user, "lys-login switch-user"))
          await_ready(machine, "username", greeters)

      def pick(user, secret):
          machine.send_chars(f"{user}\n{secret}\n")

      def lock(user, sid):
          machine.succeed(f"touch /tmp/lys-login/{user}-lock")
          machine.wait_until_succeeds(f"grep -qx locked /tmp/lys-login/{user}-lock.out", timeout=60)
          machine.wait_until_succeeds(f"[ \"$(loginctl show-session {sid} -P LockedHint)\" = yes ]", timeout=30)

      def locker(user):
          return int(machine.succeed(f"pgrep -u {user} -f 'lys-login lock$' | head -n 1").strip())

      def alive(pid, what):
          if machine.execute(f"test -e /proc/{pid}")[0] != 0:
              raise Exception(f"{what} (pid {pid}) is gone")

      try:
          machine.start()
          machine.wait_for_unit("lys-login.service")
          for user in ("alice", "bob"):
              machine.succeed(f"install -D -o {user} -m 0644 ${lockConfig} /home/{user}/.config/lys-shell/config.yaml")
          await_ready(machine, "username", 1)
          machine.send_chars("alice\n${password}\n")
          alice_sid, alice_vt = session_of("alice")
          await_vt(alice_vt, "alice's session")
          alice_worker = workers()
          lock("alice", alice_sid)
          alice_lock = locker("alice")

          switch_user("alice", 2)
          if "a greeter for another user on VT" not in journal():
              raise Exception("the daemon did not say it showed a greeter for another user")
          if active_vt() == alice_vt:
              raise Exception("the greeter for another user shows on alice's VT")
          pick("bob", "${password}")
          bob_sid, bob_vt = session_of("bob")
          if bob_vt == alice_vt:
              raise Exception("bob's session runs on alice's VT")
          await_vt(bob_vt, "bob's session")
          alive(alice_lock, "alice's lock screen")
          if machine.succeed(f"loginctl show-session {alice_sid} -P State").strip() not in ("online", "active"):
              raise Exception("alice's session stopped while bob logged in")
          machine.screenshot("01-bob")
          two = workers()
          if len(two) != 2:
              raise Exception(f"{len(two)} session workers run, want alice's and bob's")

          switch_user("bob", 3)
          pick("alice", "anything")
          machine.wait_until_succeeds("journalctl -b -u lys-login -o cat --no-pager | grep -q 'alice already has a session'", timeout=60)
          await_vt(alice_vt, "the switch back to alice")
          if workers() != two:
              raise Exception(f"picking alice started a worker for her: {workers()}, were {two}")
          if "login of alice: " in journal().split("alice already has a session")[-1]:
              raise Exception("alice was authenticated again")
          alive(alice_lock, "alice's lock screen")
          machine.screenshot("02-back-to-alice-locked")

          daemon = int(machine.succeed("systemctl show -P MainPID lys-login.service"))
          machine.succeed(UPGRADE)
          if int(machine.succeed("systemctl show -P MainPID lys-login.service")) != daemon:
              raise Exception("the upgrade changed the daemon's pid")
          if "dm: upgraded in place from" not in journal():
              raise Exception("the upgrade is not in the journal")
          for user in ("alice", "bob"):
              if f"dm: following the session of {user}" not in journal():
                  raise Exception(f"the upgraded daemon does not follow {user}'s session")
          if workers() != two:
              raise Exception(f"the upgrade changed the session workers: {workers()}, were {two}")

          machine.succeed(f"chvt {bob_vt}")
          await_vt(bob_vt, "bob's session again")
          bob_worker = [p for p in two if p not in alice_worker][0]
          compositor = int(machine.succeed(f"pgrep -P {bob_worker} | head -n 1").strip())
          machine.succeed(f"kill -TERM {compositor}")
          machine.wait_until_succeeds(f"! test -e /proc/{bob_worker}", timeout=30)
          await_vt(alice_vt, "bob's logout")
          time.sleep(2)
          if greeters():
              raise Exception("bob's logout showed a greeter instead of alice's session")
          alive(alice_lock, "alice's lock screen")

          machine.succeed("rm /tmp/lys-login/bob-session")
          switch_user("alice", 4)
          pick("bob", "${password}")
          bob_sid, bob_vt = session_of("bob")
          if len(workers()) != 2:
              raise Exception(f"{workers()} after bob's second login")
          machine.succeed("touch /tmp/lys-login/stop-agents")
          machine.execute("pkill -u alice -f 'lys-login lock'")
          begun = time.monotonic()
          machine.succeed("systemctl stop lys-login.service")
          took = time.monotonic() - begun
          if took >= 20:
              raise Exception(f"stopping with two sessions took {took:.0f}s")
          machine.wait_until_succeeds("[ -z \"$(pgrep -f '^/nix/store/[^ ]*/lys-login worker session')\" ]", timeout=15)
          for sid in (alice_sid, bob_sid):
              machine.wait_until_succeeds(f"! loginctl show-session {sid}", timeout=30)
          log = journal()
          for absent in ("panic", "dm: greeter connection:"):
              if absent in log:
                  raise Exception(f"the journal has {absent!r}")
          print(log[-4000:])
      except Exception:
          machine.screenshot("failure")
          print(machine.execute("journalctl -b --no-pager -n 300")[1])
          raise
    '';
  })
