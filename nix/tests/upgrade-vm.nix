{
  pkgs,
  lib,
  self,
  lys-wm,
  lys-secrets,
}: let
  user = "alice";
  password = "lys-login-vm";
  otherRuntimeDir = "/run/lys-login-elsewhere";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};
  first = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
  next = first.overrideAttrs (old: {version = "${old.version}-next";});

  refusing = pkgs.writeShellScriptBin "lys-login" ''
    echo "this build cannot take over: $*" >&2
    exit 3
  '';
  failsToResume = pkgs.writeShellScriptBin "lys-login" ''
    if [ "$1" = daemon-resume ] && [ "$2" != --check ]; then
      state=$3
      shift 4
      exec ${next}/bin/lys-login daemon-resume --state-fd "$state" -- --runtime-dir ${otherRuntimeDir} "$@"
    fi
    exec ${next}/bin/lys-login "$@"
  '';

  requestUpgrade =
    pkgs.writers.writePython3 "lys-login-vm-request-upgrade" {} ''
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

  lockConfig = pkgs.writeText "lys-login-vm-config.yaml" ''
    login:
      lock:
        pam-service: lys-shell
  '';
  sessionAgent = pkgs.writeShellScript "lys-login-vm-session-agent" ''
    env > /tmp/lys-login/session-env.tmp
    mv /tmp/lys-login/session-env.tmp /tmp/lys-login/session-env
    until [ -e /tmp/lys-login/end-session ]; do
      if [ -e /tmp/lys-login/lock-now ]; then
        rm /tmp/lys-login/lock-now
        lys-login lock > /tmp/lys-login/lock.out 2> /tmp/lys-login/lock.err
        echo $? > /tmp/lys-login/lock.rc
      fi
      sleep 0.1
    done
  '';

  sessionConfig = pkgs.writeText "lys-login-vm-session.yaml" ''
    binds: []
    animations:
      enabled: false
    autostart:
      - "${sessionAgent}"
  '';

  session = pkgs.writeShellScript "lys-login-vm-session" ''
    rm -f /tmp/lys-login/end-session
    exec lys-wm --config ${sessionConfig}
  '';

  staged = pkgs.writeText "lys-login-vm-staged.yaml" ''
    login:
      background-mode: color
      background-color: "#ff0000"
  '';
in
  pkgs.testers.runNixOSTest ({nodes, ...}: {
    name = "lys-login-upgrade-vm";
    globalTimeout = 540;

    nodes.machine = {config, ...}: {
      imports = [
        self.nixosModules.default
        lys-wm.nixosModules.default
        lys-secrets.nixosModules.default
        greeterVm.module
      ];

      programs.lys-wm.enable = true;
      services.lys-secrets.enable = true;
      programs.lys-login = {
        package = first;
        lock.enable = true;
        greeter = {
          enable = true;
          displayManager = "lys-login";
          compositorPackage = config.programs.lys-wm.package;
          session = {
            dirs = [];
            x11Dirs = [];
            command = "${session}";
          };
        };
      };

      specialisation = {
        refusing.configuration.programs.lys-login.package = lib.mkForce refusing;
        next.configuration.programs.lys-login = {
          package = lib.mkForce next;
          greeter.session.environment = ["LYS_UPGRADE_VM=next"];
        };
      };

      users.users.${user} = {
        isNormalUser = true;
        inherit password;
      };
    };

    testScript = ''
      import os
      import re
      import shlex
      import time

      ${greeterVm.script "-t lys-login"}

      UPGRADE_TO_FIRST = "${nodes.machine.systemd.services.lys-login.serviceConfig.ExecReload}"
      FIRST = "${first}"
      NEXT = "${next}"
      SWITCH = "/run/booted-system/specialisation/{}/bin/switch-to-configuration test"

      def daemon():
          return int(machine.succeed("systemctl show -P MainPID lys-login.service"))

      def exe(pid):
          return machine.succeed(f"readlink /proc/{pid}/exe").strip()

      def runs(pid, build, what):
          got = exe(pid)
          if not got.startswith(build + "/"):
              raise Exception(f"{what} (pid {pid}) runs {got}, want {build}")

      def worker():
          return int(machine.succeed("pgrep -f '^/nix/store/[^ ]*/lys-login worker session' | head -n 1").strip())

      def locker():
          return int(machine.succeed("pgrep -u ${user} -f 'lys-login lock$' | head -n 1").strip())

      def greeter():
          return int(machine.succeed("pgrep -u lys-greeter -f 'lys-login greet' | head -n 1").strip())

      def alive(pid, what):
          state = machine.succeed(f"ps -o stat= -p {pid}").strip()
          if state.startswith("Z"):
              raise Exception(f"{what} (pid {pid}) is a zombie")

      def journal():
          return machine.succeed("journalctl -b -u lys-login -o cat --no-pager")

      def as_user(command):
          return (
              "su ${user} -s /bin/sh -c "
              + shlex.quote(f"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/{UID}/bus XDG_RUNTIME_DIR=/run/user/{UID} {command}")
          )

      def tracked(through):
          cursor = "XCURSOR_THEME=vm XCURSOR_SIZE=32"
          machine.succeed(as_user(f"{cursor} {through}/bin/lys-login record-cursor"))
          code, out = machine.execute(f"su lys-greeter -s /bin/sh -c '{cursor} {through}/bin/lys-login record-cursor' 2>&1")
          if code == 0:
              raise Exception(f"another account recorded a cursor: {out}")

      def session_up(sid, compositor, session_worker):
          state = machine.succeed(f"loginctl show-session {sid} -P State").strip()
          if state not in ("active", "online"):
              raise Exception(f"logind's session {sid} is {state}")
          alive(compositor, "the session's compositor")
          alive(session_worker, "the session's worker")

      def collection_locked():
          return machine.execute(as_user("busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked"))[1].strip()

      def lock_collection():
          machine.succeed(as_user("busctl --user call org.freedesktop.secrets /org/freedesktop/secrets org.freedesktop.Secret.Service Lock ao 1 /org/freedesktop/secrets/collection/login"))
          machine.wait_until_succeeds(as_user("busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked | grep -qx 'b true'"), timeout=10)

      def lock():
          machine.succeed("rm -f /tmp/lys-login/lock.out /tmp/lys-login/lock.err /tmp/lys-login/lock.rc")
          machine.succeed("touch /tmp/lys-login/lock-now")
          machine.wait_until_succeeds("grep -qx locked /tmp/lys-login/lock.out", timeout=60)
          machine.wait_until_succeeds("grep -q 'lock: session locked' /tmp/lys-login/lock.err", timeout=10)
          return locker()

      def locked_hint(sid):
          return machine.succeed(f"loginctl show-session {sid} -P LockedHint").strip()

      def unlock(sid):
          end = time.monotonic() + 60
          while machine.execute("test -e /tmp/lys-login/lock.rc")[0] != 0:
              if time.monotonic() >= end:
                  raise Exception("the lock screen did not unlock:\n" + machine.succeed("cat /tmp/lys-login/lock.err"))
              machine.send_chars("${password}\n")
              time.sleep(2)
          rc = machine.succeed("cat /tmp/lys-login/lock.rc").strip()
          if rc != "0":
              raise Exception(f"the lock screen exited {rc}:\n" + machine.succeed("cat /tmp/lys-login/lock.err"))
          if locked_hint(sid) != "no":
              raise Exception("logind still has the session locked after the unlock")

      def console_shown_while(done, what):
          """Dumps the screen until done() holds and fails on a frame with the
          kernel's console on it: a lot of its text colour, which neither the
          greeter nor lys-wm draws."""
          path = os.path.join(machine.out_dir, "console-probe.ppm")
          end = time.monotonic() + 60
          while not done():
              if time.monotonic() >= end:
                  raise Exception(f"{what} did not finish")
              machine.send_monitor_command(f"screendump {path}")
              with open(path, "rb") as f:
                  data = f.read()
              header = re.match(rb"P6\s+\d+\s+\d+\s+\d+\s", data)
              assert header is not None
              body = data[header.end():]
              text = sum(1 for at in range(0, len(body) - 3, 21) if body[at:at + 3] == b"\xaa\xaa\xaa")
              if text > 1000:
                  machine.screenshot("console-shown")
                  raise Exception(f"the kernel's console shows during {what}: {text} sampled pixels of its text")
          os.remove(path)

      def session_env_written():
          return machine.execute("test -s /tmp/lys-login/session-env")[0] == 0

      def login(field, occurrence, typed, build):
          await_ready(machine, field, occurrence)
          machine.succeed("rm -f /tmp/lys-login/session-env")
          machine.send_chars(typed + "\n")
          console_shown_while(session_env_written, "the login")
          machine.wait_until_succeeds("test -s /tmp/lys-login/session-env", timeout=60)
          session_worker = worker()
          runs(session_worker, build, "the session's worker")
          env = dict(l.split("=", 1) for l in machine.succeed("cat /tmp/lys-login/session-env").splitlines() if "=" in l)
          compositor = int(machine.succeed(f"pgrep -P {session_worker}").strip())
          return env["XDG_SESSION_ID"], compositor, session_worker

      def logout(sid, compositor, session_worker):
          machine.succeed("touch /tmp/lys-login/end-session")
          machine.succeed(f"kill -TERM {compositor}")
          console_shown_while(lambda: machine.execute("pgrep -u lys-greeter -f 'lys-login greet'")[0] == 0, "the logout")
          machine.wait_until_succeeds(f"! test -e /proc/{session_worker}", timeout=30)
          machine.wait_until_succeeds(f"! loginctl show-session {sid}", timeout=30)
          machine.wait_until_succeeds("pgrep -u lys-greeter -f 'lys-login greet'", timeout=60)

      try:
          machine.start()
          machine.wait_for_unit("lys-login.service")
          machine.wait_until_succeeds("pgrep -u lys-greeter -f 'lys-login greet'", timeout=90)
          machine.succeed("install -D -o ${user} -m 0644 ${lockConfig} /home/${user}/.config/lys-shell/config.yaml")
          UID = machine.succeed("id -u ${user}").strip()
          pid = daemon()
          runs(pid, FIRST, "the daemon")
          sid, compositor, session_worker = login("username", 1, "${user}\n${password}", FIRST)
          runs(session_worker, FIRST, "the session's worker")
          machine.wait_until_succeeds(as_user("busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked | grep -qx 'b false'"), timeout=40)
          machine.screenshot("01-session")
          tracked(FIRST)
          args = machine.succeed(f"cat /proc/{pid}/cmdline").split("\0")[2:-1]
          out = machine.succeed(
              "${requestUpgrade} ${failsToResume}/bin/lys-login "
              + " ".join(shlex.quote(a) for a in args)
          )
          if "could not resume" not in out or "${otherRuntimeDir}" not in out:
              raise Exception(f"the failed resume answered {out}")
          if daemon() != pid:
              raise Exception("the failed resume changed the daemon's pid")
          runs(pid, FIRST, "the rolled-back daemon")
          if "dm: rolled back to" not in journal():
              raise Exception("the rollback is not in the journal")
          session_up(sid, compositor, session_worker)
          tracked(FIRST)
          lock_collection()
          old_locker = lock()
          runs(old_locker, FIRST, "the lock screen")
          if locked_hint(sid) != "yes":
              raise Exception("logind does not have the session locked")
          machine.screenshot("02-locked")
          code, out = machine.execute(SWITCH.format("refusing") + " 2>&1")
          print(out)
          if code == 0:
              raise Exception("the switch to a refusing build succeeded")
          if "this build cannot take over" not in out or "lys-login-upgrade-failed" not in out:
              raise Exception("the switch does not say why the upgrade failed:\n" + out)
          if "this build cannot take over" not in journal():
              raise Exception("the refusal's reason is not in the unit's journal:\n" + journal()[-3000:])
          if daemon() != pid:
              raise Exception("the refused upgrade changed the daemon's pid")
          runs(pid, FIRST, "the daemon after the refused upgrade")
          if machine.succeed("systemctl is-active lys-login.service").strip() != "active":
              raise Exception("the refused upgrade took the unit down")
          session_up(sid, compositor, session_worker)
          tracked(FIRST)
          alive(old_locker, "the lock screen")
          machine.succeed(SWITCH.format("next") + " 2>&1")
          if daemon() != pid:
              raise Exception("the upgrade changed the daemon's pid")
          runs(pid, NEXT, "the upgraded daemon")
          if "dm: upgraded in place from" not in journal():
              raise Exception("the upgrade is not in the journal:\n" + journal()[-3000:])
          session_up(sid, compositor, session_worker)
          runs(session_worker, FIRST, "the session's worker, which holds its PAM handle")
          tracked(NEXT)
          machine.succeed(as_user("lys-login apply-config ${staged}"))
          machine.succeed("grep -q ff0000 /etc/lys-shell/runtime.yaml")
          alive(old_locker, "the lock screen")
          runs(old_locker, FIRST, "the lock screen")
          if locked_hint(sid) != "yes":
              raise Exception("the upgrade dropped the lock")
          machine.fail("test -e /tmp/lys-login/lock.rc")
          machine.screenshot("03-still-locked")
          if collection_locked() != "b true":
              raise Exception("the collection unlocked before the unlock")
          unlock(sid)
          machine.wait_until_succeeds(as_user("busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked | grep -qx 'b false'"), timeout=20)
          lock_collection()
          new_locker = lock()
          runs(new_locker, NEXT, "the next lock screen")
          unlock(sid)
          machine.wait_until_succeeds(as_user("busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked | grep -qx 'b false'"), timeout=20)
          session_up(sid, compositor, session_worker)
          logout(sid, compositor, session_worker)
          next_greeter = greeter()
          runs(next_greeter, NEXT, "the next greeter")
          if "LYS_UPGRADE_VM=next" not in machine.succeed(f"tr '\\0' ' ' < /proc/{next_greeter}/cmdline"):
              raise Exception("the next greeter runs without the new configuration's arguments")
          await_ready(machine, "password", 1)
          machine.screenshot("04-next-greeter")
          if daemon() != pid:
              raise Exception("logging out changed the daemon's pid")
          machine.succeed(UPGRADE_TO_FIRST)
          runs(pid, FIRST, "the daemon upgraded at the greeter")
          machine.wait_until_succeeds(f"! test -e /proc/{next_greeter}", timeout=30)
          machine.wait_until_succeeds("pgrep -u lys-greeter -f 'lys-login greet'", timeout=60)
          runs(greeter(), FIRST, "the greeter after the upgrade at the greeter")
          sid, compositor, session_worker = login("password", 2, "${password}", FIRST)
          tracked(FIRST)
          logout(sid, compositor, session_worker)

          # Stopping the display manager ends a running session properly, in
          # far less than the service's TimeoutStopSec, and leaves nothing behind.
          sid, compositor, session_worker = login("password", 3, "${password}", FIRST)
          machine.succeed("touch /tmp/lys-login/end-session")
          begun = time.monotonic()
          machine.succeed("systemctl stop lys-login.service")
          took = time.monotonic() - begun
          if took >= 20:
              raise Exception(f"stopping with a session running took {took:.0f}s")
          machine.wait_until_succeeds(f"! test -e /proc/{session_worker}", timeout=10)
          machine.wait_until_succeeds(f"! loginctl show-session {sid}", timeout=30)
          if "ending the session on worker" not in journal():
              raise Exception("the daemon did not say it ended the session")
          if "timed out" in machine.succeed("journalctl -b -u lys-login.service --no-pager"):
              raise Exception("the stop waited out a timeout")
          machine.succeed("systemctl start lys-login.service")
          machine.wait_until_succeeds("pgrep -u lys-greeter -f 'lys-login greet'", timeout=60)

          # A daemon that crashes leaves the session running. The restarted one
          # puts no greeter on top of it, and shows one once it ends.
          sid, compositor, session_worker = login("password", 4, "${password}", NEXT)
          crashed = daemon()
          machine.succeed("systemctl kill -s KILL --kill-whom=main lys-login.service")
          machine.wait_until_succeeds(f"[ \"$(systemctl show -P MainPID lys-login.service)\" != {crashed} ] && [ \"$(systemctl show -P MainPID lys-login.service)\" != 0 ]", timeout=30)
          machine.wait_until_succeeds("journalctl -b -u lys-login -o cat --no-pager | grep -q 'the greeter waits for it to end'", timeout=30)
          alive(session_worker, "the session's worker after the crash")
          if machine.execute("pgrep -u lys-greeter -f 'lys-login greet'")[0] == 0:
              raise Exception("a greeter started on top of the live session")
          logout(sid, compositor, session_worker)

          # Stopping while waiting on such a session ends it too.
          sid, compositor, session_worker = login("password", 5, "${password}", NEXT)
          crashed = daemon()
          machine.succeed("systemctl kill -s KILL --kill-whom=main lys-login.service")
          machine.wait_until_succeeds(f"[ \"$(systemctl show -P MainPID lys-login.service)\" != {crashed} ] && [ \"$(systemctl show -P MainPID lys-login.service)\" != 0 ]", timeout=30)
          machine.succeed("touch /tmp/lys-login/end-session")
          machine.succeed("systemctl stop lys-login.service")
          machine.wait_until_succeeds(f"! test -e /proc/{session_worker}", timeout=20)
          machine.wait_until_succeeds(f"! loginctl show-session {sid}", timeout=30)
          machine.succeed("systemctl start lys-login.service")
          machine.wait_until_succeeds("pgrep -u lys-greeter -f 'lys-login greet'", timeout=60)

          log = journal()
          for absent in ("dm: greeter: the greeter ended", "dm: greeter connection:", "panic"):
              if absent in log:
                  raise Exception(f"the journal has {absent!r}:\n" + log[-4000:])
      except Exception:
          machine.screenshot("failure")
          print(machine.execute("journalctl -b --no-pager -n 300")[1])
          print(machine.execute("cat /tmp/lys-login/lock.err")[1])
          raise
    '';
  })
