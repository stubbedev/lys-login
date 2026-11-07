# Autologin under lys-login's display manager in a NixOS VM.
#
# On a plain disk the daemon logs alice in at boot with no greeter, and once
# she logs out the greeter shows. Then the root moves to a LUKS volume whose
# passphrase the initrd asks for on the console: systemd-cryptsetup leaves it in
# the kernel keyring, pam_systemd_loadkey hands it to the autologin as the
# password, and the worker opens lys-secrets' login collection with it. A boot
# with the passphrase that is alice's keyring password finds the collection open
# and `secret-tool lookup` answers with no prompt; a boot with the volume's
# other passphrase starts the session with the collection locked, one warning
# names why, and the lock screen opens it. Neither passphrase reaches an argv,
# an environment or the journal.
#
# Build: nix build .#checks.<system>.autologin-vm -L
{
  pkgs,
  lib,
  self,
  lys-wm,
  lys-secrets,
}: let
  password = "lys-autologin-vm";
  otherPassphrase = "other-luks-key";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};

  lockConfig = pkgs.writeText "lys-login-autologin-vm-config.yaml" ''
    login:
      lock:
        pam-service: lys-shell
  '';

  sessionAgent = pkgs.writeShellScript "lys-login-autologin-vm-agent" ''
    env | sort > /tmp/lys-login/session-env
    echo "$XDG_SESSION_ID" > /tmp/lys-login/session
    while :; do
      if [ -e /tmp/lys-login/lock-now ]; then
        rm /tmp/lys-login/lock-now
        lys-login lock > /tmp/lys-login/lock.out 2> /tmp/lys-login/lock.err
        echo $? > /tmp/lys-login/lock.rc
      fi
      sleep 0.1
    done
  '';

  sessionConfig = pkgs.writeText "lys-login-autologin-vm-session.yaml" ''
    binds: []
    animations:
      enabled: false
    autostart:
      - "${sessionAgent}"
  '';

  session = pkgs.writeShellScript "lys-login-autologin-vm-session" ''
    exec lys-wm --config ${sessionConfig}
  '';
in
  pkgs.testers.nixosTest {
    name = "lys-login-autologin-vm";
    globalTimeout = 900;

    nodes.machine = {...}: {
      imports = [
        self.nixosModules.default
        lys-wm.nixosModules.default
        lys-secrets.nixosModules.default
        greeterVm.module
      ];
      virtualisation = {
        emptyDiskImages = [1024];
        useBootLoader = true;
        useEFIBoot = true;
        mountHostNixStore = true;
      };
      boot.loader.systemd-boot.enable = true;
      environment.systemPackages = [pkgs.cryptsetup pkgs.libsecret];
      programs.lys-wm.enable = true;
      services.lys-secrets.enable = true;
      programs.lys-login = {
        lock.enable = true;
        autologin.user = "alice";
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
      specialisation.luks.configuration = {
        boot.initrd.luks.devices = lib.mkVMOverride {cryptroot.device = "/dev/vdb";};
        virtualisation.rootDevice = "/dev/mapper/cryptroot";
        virtualisation.fileSystems."/".autoFormat = true;
      };
    };

    testScript = ''
      import time

      ${greeterVm.script "-t lys-login"}

      SECRETS = ("${password}", "${otherPassphrase}")

      def journal():
          return machine.succeed("journalctl -b -o cat --no-pager")

      def as_alice(command):
          return f"su alice -s /bin/sh -c 'XDG_RUNTIME_DIR=/run/user/$(id -u alice) {command}'"

      def await_session():
          machine.wait_until_succeeds("test -s /tmp/lys-login/session", timeout=120)
          return machine.succeed("cat /tmp/lys-login/session").strip()

      def no_greeter_before_the_session():
          log = journal()
          if "dm: autologin of alice into" not in log:
              raise Exception("the daemon did not log alice in:\n" + log[-3000:])
          if "lys-greeter starting" in log:
              raise Exception("a greeter came up before the autologin")

      def collection_locked():
          return machine.execute(as_alice("busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked"))[1].strip()

      def secrets_nowhere():
          for secret in SECRETS:
              if secret in journal():
                  raise Exception("a passphrase is in the journal")
              machine.succeed(f"printf '%s\\n' {secret} > /tmp/lys-login/pattern")
              hits = machine.execute("grep -l -a -F -f /tmp/lys-login/pattern /proc/[0-9]*/cmdline /proc/[0-9]*/environ 2>/dev/null")[1].strip()
              machine.succeed("rm /tmp/lys-login/pattern")
              if hits:
                  raise Exception(f"a passphrase is in {hits}")

      def boot_luks(passphrase):
          machine.succeed("bootctl set-oneshot nixos-generation-1-specialisation-luks.conf")
          machine.succeed("sync")
          machine.crash()
          machine.start()
          machine.wait_for_console_text("Please enter passphrase for disk cryptroot")
          machine.send_console(passphrase + "\n")
          machine.wait_for_unit("multi-user.target")
          machine.succeed("install -D -o alice -m 0644 ${lockConfig} /home/alice/.config/lys-shell/config.yaml")

      try:
          machine.start()
          machine.wait_for_unit("lys-login.service")
          sid = await_session()
          no_greeter_before_the_session()
          if "no disk passphrase in the kernel keyring" not in journal():
              raise Exception("the worker did not say the keyring stays locked without a passphrase")
          machine.screenshot("01-autologin")
          machine.succeed("kill -TERM $(pgrep -u alice -x lys-wm || pgrep -u alice -f 'lys-wm --config')")
          machine.wait_until_succeeds(f"! loginctl show-session {sid}", timeout=60)
          await_ready(machine, "username", 1)
          machine.screenshot("02-greeter-after-logout")
          if journal().count("dm: autologin of alice into") != 1:
              raise Exception("the autologin ran again after the logout")

          machine.succeed("echo -n ${password} | cryptsetup luksFormat -q --iter-time=1 /dev/vdb -")
          machine.succeed("echo -n ${otherPassphrase} > /tmp/other && echo -n ${password} | cryptsetup luksAddKey -q --iter-time=1 --key-file=- /dev/vdb /tmp/other && rm /tmp/other")

          boot_luks("${password}")
          await_session()
          no_greeter_before_the_session()
          machine.wait_until_succeeds(as_alice("busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked | grep -qx 'b false'"), timeout=60)
          machine.succeed(as_alice("printf lys-secret | secret-tool store --label=vm service lys-vm key k"))
          secrets_nowhere()

          boot_luks("${password}")
          await_session()
          no_greeter_before_the_session()
          if "the disk passphrase from the kernel keyring goes to the login keyring" not in journal():
              raise Exception("the worker did not say it hands the passphrase over")
          machine.wait_until_succeeds(as_alice("secret-tool lookup service lys-vm key k | grep -qx lys-secret"), timeout=60)
          machine.screenshot("03-keyring-open")
          secrets_nowhere()

          boot_luks("${otherPassphrase}")
          sid = await_session()
          no_greeter_before_the_session()
          machine.wait_until_succeeds("journalctl -b -u lys-login -p warning -o cat --no-pager | grep -q 'the login keyring of alice stays locked'", timeout=60)
          warnings = machine.succeed("journalctl -b -u lys-login -p warning -o cat --no-pager | grep 'login keyring'").strip().splitlines()
          if len(warnings) != 1:
              raise Exception(f"want one warning naming the mismatch, got {warnings}")
          if collection_locked() != "b true":
              raise Exception("a passphrase that is not the keyring's opened it")
          machine.succeed("touch /tmp/lys-login/lock-now")
          machine.wait_until_succeeds("grep -qx locked /tmp/lys-login/lock.out", timeout=60)
          machine.wait_until_succeeds("grep -q 'lock: session locked' /tmp/lys-login/lock.err", timeout=10)
          end = time.monotonic() + 60
          while machine.execute("test -e /tmp/lys-login/lock.rc")[0] != 0:
              if time.monotonic() >= end:
                  raise Exception("the lock screen did not unlock:\n" + machine.succeed("cat /tmp/lys-login/lock.err"))
              machine.send_chars("${password}\n")
              time.sleep(2)
          machine.wait_until_succeeds(as_alice("secret-tool lookup service lys-vm key k | grep -qx lys-secret"), timeout=60)
          secrets_nowhere()
          print(journal()[-3000:])
      except Exception:
          machine.screenshot("failure")
          print(machine.execute("journalctl -b --no-pager -n 300")[1])
          raise
    '';
  }
