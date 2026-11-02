# lys-login under greetd in a cage kiosk, on both of wlroots' render paths
# (GLES2, over Mesa's software driver as the VM has no GPU, and pixman): the
# greeter takes keys typed the moment it says it is ready, a wrong
# password leaves it up, the right one starts the session, and the greeter
# comes back with the password field focused for the remembered user. On
# a dead-key layout the password takes a composed character, and the greeter
# finds its keyboard and locale data on all of them. An account
# AccountsService marks as a system account stays out of the list and still
# logs in by typing its name.
#
# Build: nix build .#checks.<system>.greetd-vm -L
{
  pkgs,
  self,
}: let
  user = "alice";
  password = "lys-login-vm";
  greeterVm = import ./greeter-vm.nix {inherit pkgs;};

  session = pkgs.writeShellScript "lys-login-greetd-vm-session" ''
    id -un > /tmp/lys-login/session
    sleep 2
  '';

  node = {
    renderer,
    env ? [],
  }: {
    imports = [self.nixosModules.default greeterVm.module];
    programs.lys-login.greeter = {
      enable = true;
      displayManager = "greetd";
      inherit renderer;
      graphicsWrapper = ["${pkgs.systemd}/bin/systemd-cat" "--identifier=lys-greeter"] ++ env;
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
    name = "lys-login-greetd-vm";

    nodes.gles = node {
      renderer = "auto";
      env = ["env" "WLR_RENDERER_ALLOW_SOFTWARE=1"];
    };
    nodes.pixman = node {renderer = "software";};
    nodes.hidden = {
      imports = [(node {renderer = "software";}) greeterVm.systemAccount];
    };
    nodes.deadkey = node {
      renderer = "software";
      env = ["env" "XKB_DEFAULT_LAYOUT=us" "XKB_DEFAULT_VARIANT=intl"];
    };

    testScript = ''
      ${greeterVm.script "-t lys-greeter"}

      def logs_of(machine):
          return machine.succeed("journalctl -u greetd -t lys-greeter -o cat --no-pager")

      def login_through(machine):
          machine.wait_for_unit("greetd.service")
          await_ready(machine, "username", 1)
          machine.screenshot(f"{machine.name}-01-greeter")

          machine.send_chars("${user}\nwrong-password\n")
          machine.wait_until_succeeds("journalctl -t lys-greeter -o cat --no-pager | grep -q 'greeter: authentication failed'", timeout=30)
          machine.screenshot(f"{machine.name}-02-wrong-password")
          machine.fail("test -e /tmp/lys-login/session")

          machine.send_chars("${password}\n")
          machine.wait_until_succeeds("test -s /tmp/lys-login/session", timeout=60)
          seen = machine.succeed("cat /tmp/lys-login/session").strip()
          if seen != "${user}":
              raise Exception(f"the session ran as {seen}:\n" + logs_of(machine))

          await_ready(machine, "password", 1)
          machine.screenshot(f"{machine.name}-03-greeter-again")

      def no_xkb_errors(machine):
          logged = machine.succeed("journalctl -t lys-greeter -o cat --no-pager | grep 'XKB-' || true")
          if logged:
              raise Exception(f"the greeter on {machine.name} lacks keyboard or locale data:\n{logged}")

      def composed_password(machine):
          machine.wait_for_unit("greetd.service")
          machine.succeed("printf '${user}:caf\\303\\251\\n' | chpasswd")
          await_ready(machine, "username", 1)
          machine.send_chars("${user}\ncaf'e\n")
          machine.wait_until_succeeds("test -s /tmp/lys-login/session", timeout=30)
          no_xkb_errors(machine)

      def hidden_account(machine):
          machine.wait_for_unit("greetd.service")
          await_ready(machine, "username", 1)
          logged = machine.succeed("journalctl -t lys-greeter -o cat --no-pager")
          if "not listing spare:" not in logged:
              raise Exception(f"the system account was not left out:\n{logged}")
          if "not listing ${user}:" in logged:
              raise Exception(f"the regular account was left out:\n{logged}")
          machine.screenshot("hidden-01-list")
          machine.send_chars("spare\nspare-vm\n")
          machine.wait_until_succeeds("test -s /tmp/lys-login/session", timeout=30)
          seen = machine.succeed("cat /tmp/lys-login/session").strip()
          if seen != "spare":
              raise Exception(f"typing the hidden account's name ran the session as {seen}")

      login_through(gles)
      no_xkb_errors(gles)
      gles.shutdown()
      login_through(pixman)
      no_xkb_errors(pixman)
      pixman.shutdown()
      composed_password(deadkey)
      deadkey.shutdown()
      hidden_account(hidden)
    '';
  }
