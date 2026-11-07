# The lock screen's status row and fingerprint in a NixOS VM: alice logs in
# through the display manager into a lys-wm session with German and US
# layouts, plays a track in a fake MPRIS player, and locks. The lock shows
# the player, follows a Caps Lock layout switch, and verifies through a fake
# fprintd on the VM's system bus: a finger that does not match counts as an
# attempt and scans again, a match unlocks without a password, and a
# password typed while the reader waits unlocks too and stops it.
#
# Build: nix build .#checks.<system>.lock-vm -L
{
  pkgs,
  self,
  lys-wm,
}: let
  user = "alice";
  password = "lys-lock-vm";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};
  python = pkgs.python3.withPackages (ps: [ps.dbus-python ps.pygobject3]);

  fakeFprintd = pkgs.writeScript "lys-login-vm-fake-fprintd" ''
    #!${python}/bin/python3
    import dbus
    import dbus.mainloop.glib
    import dbus.service
    from gi.repository import GLib

    BUS = "net.reactivated.Fprint"
    DEVICE = "net.reactivated.Fprint.Device"
    LOG = "/tmp/lys-login/fprintd.log"


    def record(line):
        with open(LOG, "a") as f:
            f.write(line + "\n")


    class Manager(dbus.service.Object):
        @dbus.service.method(BUS + ".Manager", out_signature="o")
        def GetDefaultDevice(self):
            return "/net/reactivated/Fprint/Device/0"


    class Device(dbus.service.Object):
        @dbus.service.method(DEVICE, in_signature="s", out_signature="as")
        def ListEnrolledFingers(self, user):
            record("list " + user)
            return ["right-index-finger"]

        @dbus.service.method(DEVICE, in_signature="s")
        def Claim(self, user):
            record("claim " + user)

        @dbus.service.method(DEVICE)
        def Release(self):
            record("release")

        @dbus.service.method(DEVICE, in_signature="s")
        def VerifyStart(self, finger):
            record("start " + finger)

        @dbus.service.method(DEVICE)
        def VerifyStop(self):
            record("stop")

        @dbus.service.signal(DEVICE, signature="sb")
        def VerifyStatus(self, result, done):
            pass

        @dbus.service.method("dev.stubbe.FakeFprint", in_signature="s")
        def Scan(self, result):
            record("scan " + result)
            self.VerifyStatus(result, True)


    dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
    bus = dbus.SystemBus()
    name = dbus.service.BusName(BUS, bus)
    Manager(bus, "/net/reactivated/Fprint/Manager")
    Device(bus, "/net/reactivated/Fprint/Device/0")
    record("ready")
    GLib.MainLoop().run()
    '';

  fakePlayer = pkgs.writeScript "lys-login-vm-fake-player" ''
    #!${python}/bin/python3
    import dbus
    import dbus.mainloop.glib
    import dbus.service
    from gi.repository import GLib

    PLAYER = "org.mpris.MediaPlayer2.Player"
    PROPS = "org.freedesktop.DBus.Properties"


    class Player(dbus.service.Object):
        status = "Playing"

        @dbus.service.method(PLAYER)
        def PlayPause(self):
            self.status = "Paused" if self.status == "Playing" else "Playing"

        @dbus.service.method(PROPS, in_signature="s", out_signature="a{sv}")
        def GetAll(self, iface):
            if iface == PLAYER:
                return {
                    "PlaybackStatus": self.status,
                    "CanGoNext": True,
                    "Metadata": dbus.Dictionary({"xesam:title": "Blue in Green", "xesam:artist": dbus.Array(["Miles Davis"], signature="s")}, signature="sv"),
                }
            return {"Identity": "Fake"}

        @dbus.service.method(PROPS, in_signature="ss", out_signature="v")
        def Get(self, iface, prop):
            return self.GetAll(iface)[prop]


    dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
    bus = dbus.SessionBus()
    name = dbus.service.BusName("org.mpris.MediaPlayer2.fake", bus)
    Player(bus, "/org/mpris/MediaPlayer2")
    GLib.MainLoop().run()
  '';

  fprintdPolicy = pkgs.writeTextDir "share/dbus-1/system.d/lys-login-vm-fprintd.conf" ''
    <!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
     "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
    <busconfig>
      <policy user="root">
        <allow own="net.reactivated.Fprint"/>
      </policy>
      <policy context="default">
        <allow send_destination="net.reactivated.Fprint"/>
      </policy>
    </busconfig>
  '';

  lockConfig = pkgs.writeText "lys-login-lock-vm-config.yaml" ''
    login:
      lock:
        pam-service: lys-shell
  '';

  sessionAgent = pkgs.writeShellScript "lys-login-lock-vm-session-agent" ''
    ${fakePlayer} &
    touch /tmp/lys-login/session-ready
    until [ -e /tmp/lys-login/end-session ]; do
      if [ -e /tmp/lys-login/lock-now ]; then
        rm /tmp/lys-login/lock-now
        lys-login lock > /tmp/lys-login/lock.out 2> /tmp/lys-login/lock.err
        echo $? > /tmp/lys-login/lock.rc
      fi
      sleep 0.1
    done
  '';

  sessionConfig = pkgs.writeText "lys-login-lock-vm-session.yaml" ''
    binds: []
    animations:
      enabled: false
    input:
      keyboard:
        layout: "de,us"
        options: "grp:caps_toggle"
    autostart:
      - "${sessionAgent}"
  '';

  session = pkgs.writeShellScript "lys-login-lock-vm-session" ''
    exec lys-wm --config ${sessionConfig}
  '';
in
  pkgs.testers.nixosTest {
    name = "lys-login-lock-vm";
    globalTimeout = 540;

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
      services.dbus.packages = [fprintdPolicy];
      systemd.services.fake-fprintd = {
        wantedBy = ["multi-user.target"];
        after = ["dbus.service"];
        serviceConfig.ExecStart = "${fakeFprintd}";
      };
      users.users.${user} = {
        isNormalUser = true;
        inherit password;
      };
    };

    testScript = ''
      import time

      ${greeterVm.script "-t lys-login"}

      def lock_log():
          return machine.succeed("cat /tmp/lys-login/lock.err")

      def await_lock_log(needle, what):
          end = time.monotonic() + 60
          while needle not in lock_log():
              if time.monotonic() >= end:
                  machine.screenshot(f"timeout-{what}")
                  raise Exception(f"no {what} ({needle!r}) in the lock's log:\n" + lock_log())
              time.sleep(0.5)

      def fprintd():
          return machine.succeed("cat /tmp/lys-login/fprintd.log").split("\n")[:-1]

      def await_fprintd(lines, what):
          end = time.monotonic() + 30
          while fprintd()[-len(lines):] != lines:
              if time.monotonic() >= end:
                  raise Exception(f"fprintd did not see {what}: {fprintd()}\n" + lock_log())
              time.sleep(0.3)

      def scan(result):
          machine.succeed(f"busctl call net.reactivated.Fprint /net/reactivated/Fprint/Device/0 dev.stubbe.FakeFprint Scan s {result}")

      def lock():
          machine.succeed("rm -f /tmp/lys-login/lock.out /tmp/lys-login/lock.err /tmp/lys-login/lock.rc")
          machine.succeed("touch /tmp/lys-login/lock-now")
          machine.wait_until_succeeds("grep -qx locked /tmp/lys-login/lock.out", timeout=60)
          await_lock_log("lock: session locked", "lock")
          await_fprintd(["claim ${user}", "start any"], "the lock's verification")

      def await_unlocked(what):
          machine.wait_until_succeeds("test -e /tmp/lys-login/lock.rc", timeout=60)
          rc = machine.succeed("cat /tmp/lys-login/lock.rc").strip()
          if rc != "0":
              raise Exception(f"{what}: the lock screen exited {rc}:\n" + lock_log())

      machine.start()
      machine.wait_for_unit("fake-fprintd.service")
      machine.wait_until_succeeds("grep -qx ready /tmp/lys-login/fprintd.log", timeout=30)
      machine.wait_for_unit("lys-login.service")
      machine.succeed("install -D -o ${user} -m 0644 ${lockConfig} /home/${user}/.config/lys-shell/config.yaml")
      await_ready(machine, "username", 1)
      machine.send_chars("${user}\n${password}\n")
      machine.wait_until_succeeds("test -e /tmp/lys-login/session-ready", timeout=90)
      machine.wait_until_succeeds("su ${user} -c 'XDG_RUNTIME_DIR=/run/user/$(id -u) busctl --user status org.mpris.MediaPlayer2.fake'", timeout=30)

      lock()
      machine.screenshot("01-locked")
      await_lock_log('lock: now playing from "org.mpris.MediaPlayer2.fake"', "the player")
      await_lock_log('lock: keyboard layout "German"', "the first layout")

      machine.send_key("caps_lock")
      await_lock_log('lock: keyboard layout "English (US)"', "the switched layout")
      machine.screenshot("02-layout-switched")

      scan("verify-no-match")
      await_lock_log("lock: fingerprint did not match (attempts 1)", "the counted no-match")
      await_fprintd(["scan verify-no-match", "stop", "start any"], "a new scan after the no-match")
      machine.fail("test -e /tmp/lys-login/lock.rc")
      machine.screenshot("03-no-match")

      scan("verify-match")
      await_unlocked("the fingerprint match")
      await_fprintd(["scan verify-match", "stop", "release"], "the match's stop and release")
      if "lock: authentication succeeded" in lock_log():
          raise Exception("the match ran a password conversation:\n" + lock_log())

      lock()
      machine.send_chars("${password}\n")
      await_unlocked("the password")
      if "lock: authentication succeeded" not in lock_log():
          raise Exception("the password did not unlock:\n" + lock_log())
      await_fprintd(["stop", "release"], "the password unlock stopping the reader")
      print(lock_log())
    '';
  }
