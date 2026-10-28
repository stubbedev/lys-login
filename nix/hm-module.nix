self: {
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.programs.lys-login;
  yamlFormat = pkgs.formats.yaml {};

  # A self-contained launch wrapper for the greeter: `cage -s -- lys-login greet`
  # with the render env baked in, passing any extra args straight through. Point
  # your (non-NixOS) greetd at this one path — or, for GPU acceleration, wrap it
  # with nixGL: `nixGLIntel lys-greeter-session --config /etc/lys-shell/config.yaml
  # --state /var/lib/lys-greeter/last-session`. `renderer = "software"` bakes
  # the driver-free pixman/cairo path so it runs without nixGL at all.
  # cage's own cursor (shown until the greeter maps) follows XCURSOR_*; mirror
  # the greeter's cursor config so both cursors match.
  # The former `greeter.*` cursor keys are read too, as the greeter still
  # reads them.
  greeterCursor = (cfg.greeter.settings.greeter or {}) // (cfg.greeter.settings.login.greeter or {});
  greeterCursorEnv =
    "XCURSOR_SIZE=${toString (greeterCursor.cursor-size or 24)}"
    + lib.optionalString (
      (greeterCursor.cursor-theme or "") != ""
    ) " XCURSOR_THEME=${greeterCursor.cursor-theme}";

  greeterSession = pkgs.writeShellScriptBin "lys-greeter-session" ''
    exec env ${greeterCursorEnv} ${
      lib.optionalString (cfg.greeter.renderer == "software")
      "WLR_RENDERER=pixman WLR_DRM_NO_MODIFIERS=1 GSK_RENDERER=cairo GDK_DISABLE=gl "
    }${lib.getExe cfg.greeter.cagePackage} -s -- \
      ${lib.getExe cfg.package} greet "$@"
  '';
in {
  options.programs.lys-login = {
    enable = lib.mkEnableOption ''
      lys-login on the PATH, the lock screen lys-shell starts (`lys-login
      lock`) for its lock triggers. `greeter.enable` installs it too'';

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "lys-login.packages.\${system}.default";
      description = "The lys-login package: the greeter, the lock screen and the display manager.";
    };

    lock = {
      enable = lib.mkEnableOption ''
        guidance for the session lock's PAM service. `lys-login lock` locks the
        session natively via `ext-session-lock-v1`; authenticating the unlock
        needs a PAM service, which home-manager cannot create (PAM lives under
        {file}`/etc/pam.d`, root-owned). Enabling this emits a warning with
        instructions. On NixOS use the system module's `programs.lys-login.lock`,
        which provisions the service for you'';

      pamService = lib.mkOption {
        type = lib.types.str;
        default = "lys-shell";
        description = ''
          Name of the PAM service the unlock authenticates against. Must match
          `login.lock.pam-service` in your lys-shell config. The config default is
          `system-auth` (exists on Arch/Fedora); on other distros create
          {file}`/etc/pam.d/<this name>` and point the config at it.
        '';
      };
    };

    greeter = {
      enable = lib.mkEnableOption ''
        lys-login greeter tooling: installs the kiosk compositor (cage) and writes a
        themed greeter config. NOTE: greetd is a system service home-manager
        cannot manage — on NixOS use the system module's
        `programs.lys-login.greeter`; elsewhere configure greetd yourself'';

      cagePackage = lib.mkOption {
        type = lib.types.package;
        default = pkgs.cage;
        defaultText = lib.literalExpression "pkgs.cage";
        description = "Kiosk compositor that hosts the greeter under greetd.";
      };

      renderer = lib.mkOption {
        type = lib.types.enum [
          "auto"
          "software"
        ];
        default = "auto";
        description = ''
          Render path baked into the {command}`lys-greeter-session` wrapper.
          `auto` sets nothing (cage's GLES2 and GTK's GL renderer each fall back
          to software on their own); `software` forces the driver-free pixman +
          cairo path, so the wrapper runs without nixGL on a non-NixOS host. Use
          `auto` together with a nixGL wrapper when you want GPU acceleration.
        '';
      };

      settings = lib.mkOption {
        type = yamlFormat.type;
        default = {};
        example = lib.literalExpression ''
          {
            styling.appearance = "dark";
            login.background-mode = "color";
            login.background-color = "#1e1e2e";
          }
        '';
        description = ''
          Theme/background/clock config written to
          {file}`$XDG_CONFIG_HOME/lys-shell/greeter.yaml`. The greeter runs as the
          greetd user, so copy this somewhere that user can read (e.g.
          {file}`/etc/lys-shell/config.yaml`) and point greetd's command at it:
          {command}`cage -s -- lys-login greet --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session`.
          The greeter discovers Wayland sessions from the standard
          {file}`wayland-sessions` dirs; add `--sessions DIR` to override.
        '';
      };
    };
  };

  config = {
    home.packages =
      lib.optional (cfg.enable || cfg.greeter.enable) cfg.package
      ++ lib.optionals cfg.greeter.enable [
        cfg.greeter.cagePackage
        greeterSession
      ];

    warnings =
      lib.optional cfg.greeter.enable ''
        programs.lys-login.greeter (home-manager) installs cage + the
        `lys-greeter-session` wrapper, but cannot configure greetd (a system
        service). On NixOS use programs.lys-login.greeter in the system module;
        otherwise enable greetd yourself and point its command at the wrapper:
          lys-greeter-session --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
        For GPU acceleration on a non-NixOS host, wrap it with nixGL:
          nixGLIntel lys-greeter-session --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
        (or set greeter.renderer = "software" to run driver-free without nixGL).
        The greeter discovers Wayland sessions from the standard
        wayland-sessions dirs and remembers the last pick under --state (make
        that dir writable by the greetd user). It runs as the greetd user, so
        place the config where that user can read it (e.g. /etc/lys-shell/config.yaml).
      ''
      # PAM is root-owned (/etc/pam.d); home-manager cannot create it. Point the
      # consumer at the manual step the system module does automatically.
      ++ lib.optional cfg.lock.enable ''
        programs.lys-login.lock (home-manager) cannot create the PAM service the
        unlock authenticates against (/etc/pam.d is root-owned). Create
        /etc/pam.d/${cfg.lock.pamService} yourself — this stack authenticates
        the unlock AND unlocks the GNOME `login` keyring (pam_unix must be
        `required`, not `sufficient`: a sufficient pass short-circuits the
        stack and skips the keyring hook below it; keyring lines are
        `optional`, so without gnome-keyring installed they no-op):
          auth     required   pam_unix.so
          auth     optional   pam_gnome_keyring.so
          account  required   pam_unix.so
          password required   pam_unix.so
          password optional   pam_gnome_keyring.so use_authtok
          session  required   pam_unix.so
          session  optional   pam_gnome_keyring.so auto_start
        (pam_gnome_keyring.so is packaged as libpam-gnome-keyring on
        Debian/Ubuntu, bundled with gnome-keyring on Arch/Fedora.)
        — and set `login.lock.pam-service = "${cfg.lock.pamService}"` in your lys-shell
        config. On Arch/Fedora the config default `system-auth` already exists;
        set login.lock.pam-service to that instead and skip this. On NixOS use the
        system module's programs.lys-login.lock, which provisions it for you.
      '';

    xdg.configFile."lys-shell/greeter.yaml" = lib.mkIf (cfg.greeter.enable && cfg.greeter.settings != {}) {
      source = yamlFormat.generate "lys-greeter-config.yaml" cfg.greeter.settings;
    };
  };
}
