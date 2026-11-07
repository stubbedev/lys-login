# The login keyring through lys-login's display manager, with the real
# gnome-keyring, no PAM module for it, and a session with no keyring client,
# so only the daemon can start and unlock it (#17). A login against an existing
# login.keyring reports the login collection unlocked. A login password the
# keyring no longer opens with leaves it locked and warns with its denial, and
# no keyring at all warns too, both where `journalctl -u lys-login -p warning`
# shows it.
#
# Build: nix build .#checks.<system>.keyring-vm -L
{
  pkgs,
  self,
  lys-wm,
}: let
  user = "alice";
  password = "lys-login-vm";
  newPassword = "changed-password";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};

  session = pkgs.writeShellScript "lys-login-keyring-vm-session" ''
    echo up > /tmp/lys-login/session
    sleep infinity
  '';

  loginCollectionLocked =
    pkgs.writers.writePython3 "lys-login-keyring-vm-locked" {
      libraries = [pkgs.python3Packages.jeepney];
    } ''
      import sys

      from jeepney import DBusAddress, MessageType, new_method_call
      from jeepney.io.blocking import open_dbus_connection

      SECRETS = "org.freedesktop.secrets"
      service = DBusAddress(
          "/org/freedesktop/secrets",
          bus_name=SECRETS,
          interface="org.freedesktop.Secret.Service",
      )
      collection = DBusAddress(
          "/org/freedesktop/secrets/collection/login",
          bus_name=SECRETS,
          interface="org.freedesktop.DBus.Properties",
      )


      def call(conn, message):
          reply = conn.send_and_get_reply(message, timeout=10)
          if reply.header.message_type != MessageType.method_return:
              sys.exit(f"{message.header.fields}: {reply.body}")
          return reply.body


      with open_dbus_connection("SESSION") as conn:
          call(conn, new_method_call(service, "ReadAlias", "s", ("default",)))
          (signature, locked), = call(conn, new_method_call(
              collection,
              "Get",
              "ss",
              ("org.freedesktop.Secret.Collection", "Locked"),
          ))
      if signature != "b":
          sys.exit(f"Locked has signature {signature}, not a boolean")
      print("b true" if locked else "b false")
    '';

  node = keyring: {...}: {
    imports = [
      self.nixosModules.default
      lys-wm.nixosModules.default
      greeterVm.module
    ];
    programs.lys-wm.enable = true;
    services.gnome.gnome-keyring.enable = keyring;
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
      inherit password;
    };
  };
in
  pkgs.testers.nixosTest {
    name = "lys-login-keyring-vm";
    globalTimeout = 1200;

    nodes.withkeyring = node true;
    nodes.nokeyring = node false;

    testScript = ''
      import json

      ${greeterVm.script "-t lys-login"}

      def login(m, field, occurrence, typed):
          m.wait_for_unit("lys-login.service")
          await_ready(m, field, occurrence)
          m.send_chars(typed + "\n")
          m.wait_until_succeeds("test -s /tmp/lys-login/session", timeout=60)

      def logout(m):
          m.succeed("rm /tmp/lys-login/session")
          m.succeed("loginctl terminate-user ${user}")
          m.wait_until_succeeds("! pgrep -u ${user}", timeout=60)

      def warnings(m):
          rows = m.succeed("journalctl -b -u lys-login -p warning -o json --no-pager").splitlines()
          return [r for r in map(json.loads, rows) if "stays locked" in r["MESSAGE"]]

      def await_warning(m, reason):
          m.wait_until_succeeds("journalctl -b -u lys-login -p warning -o cat --no-pager | grep -q 'stays locked'", timeout=100)
          found = warnings(m)
          if len(found) != 1:
              raise Exception(f"want one warning, got {found}")
          row = found[0]
          if reason not in row["MESSAGE"]:
              raise Exception(f"the warning does not say {reason!r}: {row['MESSAGE']}")
          if row["_SYSTEMD_UNIT"] != "lys-login.service" or row["PRIORITY"] != "4":
              raise Exception(f"the warning is not at warning in the unit's log: {row}")
          print("warning:", row["MESSAGE"])

      def collection_locked(uid):
          return (
              f"su ${user} -s /bin/sh -c 'DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/{uid}/bus ${loginCollectionLocked}'"
          )

      withkeyring.start()
      login(withkeyring, "username", 1, "${user}\n${password}")
      uid = withkeyring.succeed("id -u ${user}").strip()
      withkeyring.fail("grep -q pam_gnome_keyring /etc/pam.d/lys-login")
      control = f"/run/user/{uid}/keyring/control"

      # Nothing in the session asks for the keyring: the socket can only be
      # the daemon's doing, and it has to appear before anything here touches
      # the bus, which would activate the keyring itself.
      withkeyring.wait_until_succeeds(f"test -S {control}", timeout=60)
      withkeyring.wait_until_succeeds("test -s /home/${user}/.local/share/keyrings/login.keyring", timeout=30)
      if warnings(withkeyring):
          raise Exception(f"the first login warned: {warnings(withkeyring)}")

      # A login.keyring from an earlier session, as on a real host.
      logout(withkeyring)
      login(withkeyring, "password", 1, "${password}")
      withkeyring.wait_until_succeeds(f"test -S {control}", timeout=60)
      withkeyring.wait_until_succeeds(collection_locked(uid) + " | grep -qx 'b false'", timeout=30)
      if warnings(withkeyring):
          raise Exception(f"the login with the keyring's password warned: {warnings(withkeyring)}")
      withkeyring.screenshot("session")

      # The login password no longer opens it.
      logout(withkeyring)
      withkeyring.succeed("echo '${user}:${newPassword}' | chpasswd")
      login(withkeyring, "password", 2, "${newPassword}")
      await_warning(withkeyring, "denied (keyring password differs from login password)")
      withkeyring.wait_until_succeeds(f"test -S {control}", timeout=30)
      withkeyring.succeed(collection_locked(uid) + " | grep -qx 'b true'")
      withkeyring.shutdown()

      # No keyring installed: the same warning, naming what it found.
      nokeyring.start()
      login(nokeyring, "username", 1, "${user}\n${password}")
      await_warning(nokeyring, "neither lys-secrets nor gnome-keyring is running")
    '';
  }
