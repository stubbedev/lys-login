# A password changed through lys-login moves lys-secrets' login collection
# with it (stubbedev/lys-secrets#12), in a NixOS VM. alice logs in once, which
# makes her login collection on her password, and stores a secret. Her password
# is then expired: the next login asks for her current password and a new one, PAM changes it, and
# the worker re-keys the collection from the old password to the new one, so
# `secret-tool lookup` answers at once. The login after that, with the new
# password, opens the collection with no warning.
#
# Build: nix build .#checks.<system>.rekey-vm -L
{
  pkgs,
  self,
  lys-wm,
  lys-secrets,
}: let
  password = "lys-rekey-vm-old";
  newPassword = "Lys-rekey-VM-new-2026";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};

  session = pkgs.writeShellScript "lys-login-rekey-vm-session" ''
    echo "$XDG_SESSION_ID" > /tmp/lys-login/session
    until [ -e /tmp/lys-login/end-session ]; do sleep 0.2; done
    rm -f /tmp/lys-login/end-session /tmp/lys-login/session
  '';
in
  pkgs.testers.nixosTest {
    name = "lys-login-rekey-vm";
    globalTimeout = 600;

    nodes.machine = {...}: {
      imports = [
        self.nixosModules.default
        lys-wm.nixosModules.default
        lys-secrets.nixosModules.default
        greeterVm.module
      ];
      environment.systemPackages = [pkgs.libsecret];
      programs.lys-wm.enable = true;
      services.lys-secrets.enable = true;
      programs.lys-login.greeter = {
        enable = true;
        settings.login.greeter.show-user-list = false;
        session = {
          dirs = [];
          x11Dirs = [];
          command = "${session}";
        };
      };
      users.users.alice = {
        isNormalUser = true;
        inherit password;
      };
    };

    testScript = ''
      ${greeterVm.script "-t lys-login"}

      def journal():
          return machine.succeed("journalctl -b -o cat --no-pager")

      def as_alice(command):
          return f"su alice -s /bin/sh -c 'export XDG_RUNTIME_DIR=/run/user/$(id -u alice); {command}'"

      def unlocked():
          machine.wait_until_succeeds(as_alice("busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked | grep -qx \"b false\""), timeout=60)

      def logout():
          machine.succeed("touch /tmp/lys-login/end-session")
          machine.wait_until_succeeds("! test -e /tmp/lys-login/session", timeout=60)

      try:
          machine.start()
          machine.wait_for_unit("lys-login.service")
          await_ready(machine, "username", 1)
          machine.send_chars("alice\n${password}\n")
          machine.wait_until_succeeds("test -s /tmp/lys-login/session", timeout=90)
          unlocked()
          machine.succeed(as_alice("printf lys-secret | secret-tool store --label=vm service lys-vm key k"))
          logout()

          machine.succeed("chage -d 0 alice")
          await_ready(machine, "password", 1)
          machine.send_chars("${password}\n")
          for answer in ("${password}", "${newPassword}", "${newPassword}"):
              machine.sleep(4)
              machine.send_chars(answer + "\n")
          machine.wait_until_succeeds("test -s /tmp/lys-login/session", timeout=90)
          if "password changed for alice" not in journal():
              raise Exception("PAM did not change alice's password")
          machine.wait_until_succeeds("journalctl -b -o cat --no-pager | grep -q 'lys-secrets moved the login collection to the new password'", timeout=60)
          unlocked()
          machine.succeed(as_alice("secret-tool lookup service lys-vm key k | grep -qx lys-secret"))
          logout()

          await_ready(machine, "password", 2)
          machine.send_chars("${newPassword}\n")
          machine.wait_until_succeeds("test -s /tmp/lys-login/session", timeout=90)
          unlocked()
          machine.succeed(as_alice("secret-tool lookup service lys-vm key k | grep -qx lys-secret"))
          if "login keyring of alice stays locked" in journal():
              raise Exception("a login warned that the keyring stayed locked")
          print(journal()[-3000:])
      except Exception:
          machine.screenshot("failure")
          print(machine.execute("journalctl -b --no-pager -n 300")[1])
          raise
    '';
  }
