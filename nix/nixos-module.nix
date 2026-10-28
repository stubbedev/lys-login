self: {
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.programs.lys-login;
  yamlFormat = pkgs.formats.yaml {};

  # Directory the greeter remembers the last-selected session in. Must be
  # writable by the greetd user (`greeter`); the tmpfiles rule below creates it.
  greeterStateDir = "/var/lib/lys-greeter";

  # Forces both render layers onto their software paths: wlroots' pixman
  # renderer (DRM dumb buffers, no EGL/GBM) for cage, and GTK's cairo GSK
  # renderer (no GL context) for the greeter client. This needs no GPU vendor
  # driver at all, so it always renders — on a VM, a driverless box, or a
  # non-NixOS install without nixGL. `renderer = "auto"` omits these and lets
  # each layer try acceleration and fall back to software on its own.
  softwareRenderEnv = [
    "WLR_RENDERER=pixman"
    "WLR_DRM_NO_MODIFIERS=1"
    "GSK_RENDERER=cairo"
    "GDK_DISABLE=gl"
  ];

  # cage draws its own cursor until the greeter window maps (and wlroots scales
  # it per output). Give both cage and the GTK greeter the same cursor as the
  # rest of the system.
  #
  # The greeter runs as the unprivileged `greeter` user (home /var/empty) and
  # cannot read a normal user's often-0700 home, so a cursor theme installed only
  # via home-manager is invisible to it — with no theme resolvable it shows the
  # large built-in fallback cursor. So we don't invent a lys-shell-specific cursor
  # knob: we inherit the system-wide cursor the user already configures
  # (`environment.sessionVariables`, falling back to `environment.variables`) and
  # point XCURSOR_PATH at the system icon dir, where a theme installed via
  # `environment.systemPackages` lands. Set your cursor there — install the theme
  # system-wide and export XCURSOR_THEME/XCURSOR_SIZE — and the greeter follows.
  # An explicit `greeter.settings.login.greeter.cursor-theme`/`cursor-size`
  # still wins; the former `greeter.settings.greeter.*` keys are read too, as
  # the greeter itself still reads them.
  # sessionVariables wins over variables (that's the layer users set cursor in).
  sysVars = config.environment.variables // config.environment.sessionVariables;
  greeterSettings = (cfg.greeter.settings.greeter or {}) // (cfg.greeter.settings.login.greeter or {});
  cursorName =
    if (greeterSettings.cursor-theme or "") != ""
    then greeterSettings.cursor-theme
    else (sysVars.XCURSOR_THEME or "");
  cursorSize =
    if (greeterSettings.cursor-size or null) != null
    then toString greeterSettings.cursor-size
    else (sysVars.XCURSOR_SIZE or "24");
  cursorEnv =
    [
      "XCURSOR_SIZE=${toString cursorSize}"
      "XCURSOR_PATH=/run/current-system/sw/share/icons"
    ]
    ++ lib.optional (cursorName != "") "XCURSOR_THEME=${cursorName}";

  # The greetd `default_session` command: a kiosk compositor (cage) hosting the
  # lys-login greeter. The greeter discovers Wayland sessions from `session.dirs`,
  # lets the user pick one, and remembers the choice under `greeterStateDir`.
  # cage's `-s` allows VT switching. The greeter reads its theme from
  # /etc/lys-shell/config.yaml (written below from `greeter.settings`).
  #
  # Layering, outermost first: [graphicsWrapper] env[software vars] cage → greeter.
  # The graphicsWrapper (e.g. nixGL) must wrap the whole thing so it injects the
  # host GPU driver into cage *and* the greeter it spawns.
  greeterCommand = let
    cageExe = lib.getExe cfg.greeter.cagePackage;
    greeterExe = lib.getExe' cfg.package "lys-login";
    envArgs =
      lib.concatMapStringsSep " " (
        e: "--env ${lib.escapeShellArg e}"
      )
      cfg.greeter.session.environment;
    sessionDirArgs =
      lib.concatMapStringsSep " " (
        d: "--sessions ${lib.escapeShellArg d}"
      )
      cfg.greeter.session.dirs;
    xsessionDirArgs =
      lib.concatMapStringsSep " " (
        d: "--xsessions ${lib.escapeShellArg d}"
      )
      cfg.greeter.session.x11Dirs;
    # Optional explicit fallback session, appended as a "Custom" entry.
    fallback = lib.optionalString (
      cfg.greeter.session.command != ""
    ) "-- ${cfg.greeter.session.command}";
    wrapperPrefix = lib.optionalString (
      cfg.greeter.graphicsWrapper != []
    ) (lib.concatStringsSep " " cfg.greeter.graphicsWrapper + " ");
    envPrefix =
      "env "
      + lib.concatStringsSep " " (
        cursorEnv ++ lib.optionals (cfg.greeter.renderer == "software") softwareRenderEnv
      )
      + " ";
  in
    "${wrapperPrefix}${envPrefix}${cageExe} -s -- "
    + "${greeterExe} greet --config /etc/lys-shell/config.yaml "
    + "--state ${greeterStateDir}/last-session ${sessionDirArgs} ${xsessionDirArgs} ${envArgs} ${fallback}";
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
        a PAM service for the session lock. `lys-login lock` locks the session
        natively via `ext-session-lock-v1` (the `lock` CLI / logind `Lock`
        signal); authenticating the unlock needs a PAM service, which NixOS does
        not provide by default. This declares one named `lock.pamService`'';

      pamService = lib.mkOption {
        type = lib.types.str;
        default = "lys-shell";
        description = ''
          Name of the PAM service created for unlock authentication. Must match
          `login.lock.pam-service` in your lys-shell config (the config default is
          `system-auth`, which exists on Arch/Fedora but not NixOS — point it at
          this service, or set this to a service that already exists).
        '';
      };
    };

    greeter = {
      enable = lib.mkEnableOption ''
        the lys-login greeter: a login screen that shares the desktop/lock
        theme and lets the user pick a Wayland session. Enables `services.greetd`
        with a kiosk compositor (cage) hosting the greeter; on login it starts
        the selected session (discovered from `greeter.session.dirs`)'';

      displayManager = lib.mkOption {
        type = lib.types.enum [
          "greetd"
          "lys-login"
        ];
        default = "greetd";
        description = ''
          What runs the greeter. `greetd` runs it under greetd in a cage kiosk.
          `lys-login` is lys-login's own display manager: a root daemon on
          `vt` that shows the greeter in a lys-wm kiosk (`compositorPackage`)
          and starts the session a user logs in to through PAM (the
          `lys-login` service), with neither greetd nor cage.
        '';
      };

      compositorPackage = lib.mkOption {
        type = lib.types.nullOr lib.types.package;
        default = null;
        example = lib.literalExpression "config.programs.lys-wm.package";
        description = "lys-wm, the kiosk compositor the `lys-login` display manager shows the greeter in.";
      };

      vt = lib.mkOption {
        type = lib.types.ints.positive;
        default = 1;
        description = "Virtual terminal the `lys-login` display manager runs on.";
      };

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
          Render path for the greeter stack.

          `auto` sets nothing: wlroots (cage) tries its GLES2 renderer and GTK
          tries its GL renderer, each falling back to software on failure — so
          you get acceleration where a GPU driver is present and software
          otherwise. `software` forces both onto their software paths (wlroots
          pixman + GTK cairo), which needs no GPU driver at all. Use `software`
          on VMs, headless-ish boxes, or any host where GL init is unreliable and
          you would rather guarantee the login screen renders.
        '';
      };

      graphicsWrapper = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [];
        example = lib.literalExpression ''[ "''${pkgs.nixgl.nixGLIntel}/bin/nixGLIntel" ]'';
        description = ''
          Command prefix wrapping the whole greeter launch (cage + greeter).
          Unused on NixOS, where drivers come from {file}`/run/opengl-driver`.
          Its purpose is nixGL on non-NixOS hosts: set this to a nixGL wrapper so
          cage — and the greeter it spawns — find the host's GPU driver and run
          accelerated. Leave empty (with `renderer = "software"`) to skip GL
          entirely instead.
        '';
      };

      session = {
        dirs = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = ["${config.services.displayManager.sessionData.desktops}/share/wayland-sessions"];
          defaultText = lib.literalExpression ''["''${config.services.displayManager.sessionData.desktops}/share/wayland-sessions"]'';
          description = ''
            Directories scanned for `*.desktop` Wayland session files to offer in
            the picker. Defaults to the aggregate NixOS sessions directory, which
            collects the session files of every installed Wayland compositor.
          '';
        };

        x11Dirs = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = ["${config.services.displayManager.sessionData.desktops}/share/xsessions"];
          defaultText = lib.literalExpression ''["''${config.services.displayManager.sessionData.desktops}/share/xsessions"]'';
          description = ''
            Directories scanned for `*.desktop` X11 session files, offered with
            an "(X11)" label and launched through `startx` (greetd does not
            manage an X server itself, so `startx` must be on the session PATH —
            typically via `services.xserver.enable`). Set to `[]` to offer
            Wayland sessions only.
          '';
        };

        command = lib.mkOption {
          type = lib.types.str;
          default = "";
          example = lib.literalExpression ''"''${pkgs.niri}/bin/niri --session"'';
          description = ''
            Optional explicit fallback session, offered as a "Custom" entry in
            the picker (and the only session when `session.dirs` yields none).
            Usually unnecessary — installed compositors provide their own session
            files automatically.
          '';
        };

        environment = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [];
          example = ["XDG_SESSION_TYPE=wayland"];
          description = "Extra `KEY=value` environment entries for the session.";
        };
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
          Theme/background/clock config written to {file}`/etc/lys-shell/config.yaml`
          and read by the greeter. Uses the full lys-shell config schema; the
          greeter honours `styling`, `login.*` (background, blur, clock, and
          `login.greeter` for the cursor, user list and power buttons), and
          `wallpaper`. Leave empty to use the
          built-in defaults.
        '';
      };
    };
  };

  config = lib.mkMerge [
    (lib.mkIf (cfg.enable || cfg.greeter.enable) {
      environment.systemPackages = [cfg.package];
    })

    (lib.mkIf cfg.lock.enable {
      # The lock surface authenticates the unlock against this PAM service.
      # The NixOS default stack (pam_unix password auth) is what an unlock
      # needs; enableGnomeKeyring additionally forwards the password to a
      # running gnome-keyring-daemon so unlocking the session also unlocks the
      # `login` keyring. That matters on autologin setups (e.g. greetd
      # initial_session straight into a locked compositor): no password is ever
      # typed at login, so the lock screen's unlock is the only PAM interaction that
      # can unlock the keyring — without this, secret-service clients (Chrome,
      # ...) prompt for the keyring password on first use every session. The
      # lines are `optional`, so with no keyring daemon running they no-op.
      # With lys-secrets' services.lys-secrets.enable the lock screen hands
      # the password to lys-secrets itself after the unlock, and the
      # gnome-keyring lines stay unused.
      # The user's lys-shell config `login.lock.pam-service` must name this same service.
      security.pam.services.${cfg.lock.pamService}.enableGnomeKeyring = !(config.services.lys-secrets.enable or false);
    })

    (lib.mkIf (cfg.greeter.enable && cfg.greeter.displayManager == "greetd") {
      # greetd runs the kiosk compositor + greeter as the unprivileged greeter
      # user. mkDefault so an explicit greetd config wins.
      services.greetd = {
        enable = true;
        settings.default_session.command = lib.mkDefault greeterCommand;
      };

      # Persist the last-selected session. Owned by the greetd user (`greeter`)
      # so the greeter can write it; 0700 since only that user needs it.
      systemd.tmpfiles.rules = [
        "d ${greeterStateDir} 0700 greeter greeter -"
      ];

      environment.systemPackages = [cfg.greeter.cagePackage];
    })

    (lib.mkIf (cfg.greeter.enable && cfg.greeter.displayManager == "lys-login") {
      assertions = [
        {
          assertion = cfg.greeter.compositorPackage != null;
          message = ''programs.lys-login.greeter.displayManager = "lys-login" needs programs.lys-login.greeter.compositorPackage (lys-wm).'';
        }
      ];
      services.displayManager.enable = true;
      users.users.lys-greeter = {
        isSystemUser = true;
        group = "lys-greeter";
        home = greeterStateDir;
      };
      users.groups.lys-greeter = {};
      # One PAM service for both: the greeter's session (class greeter, no
      # authentication) and each login. startSession adds pam_systemd, which
      # registers both with logind on the seat and VT the daemon hands it.
      security.pam.services.lys-login.startSession = true;
      systemd.tmpfiles.rules = [
        "d ${greeterStateDir} 0700 lys-greeter lys-greeter -"
      ];
      systemd.defaultUnit = "graphical.target";
      systemd.services.lys-login = {
        description = "lys-login display manager";
        aliases = ["display-manager.service"];
        wantedBy = ["graphical.target"];
        wants = ["systemd-user-sessions.service"];
        after = [
          "systemd-user-sessions.service"
          "plymouth-quit-wait.service"
          "getty@tty${toString cfg.greeter.vt}.service"
        ];
        conflicts = ["getty@tty${toString cfg.greeter.vt}.service"];
        restartIfChanged = false;
        serviceConfig = {
          ExecStart = lib.escapeShellArgs (
            [
              (lib.getExe' cfg.package "lys-login")
              "daemon"
              "--vt"
              (toString cfg.greeter.vt)
              "--service"
              "lys-login"
              "--greeter-user"
              "lys-greeter"
              "--compositor"
              (lib.getExe' cfg.greeter.compositorPackage "lys-wm")
              "--"
              "--config"
              "/etc/lys-shell/config.yaml"
              "--state"
              "${greeterStateDir}/last-session"
            ]
            ++ lib.concatMap (d: [
              "--sessions"
              d
            ])
            cfg.greeter.session.dirs
            ++ lib.concatMap (d: [
              "--xsessions"
              d
            ])
            cfg.greeter.session.x11Dirs
            ++ lib.concatMap (e: [
              "--env"
              e
            ])
            cfg.greeter.session.environment
            ++ lib.optionals (cfg.greeter.session.command != "") [
              "--"
              "/bin/sh"
              "-c"
              cfg.greeter.session.command
            ]
          );
          Restart = "always";
          RestartSec = 1;
          Type = "idle";
          KeyringMode = "shared";
          IgnoreSIGPIPE = false;
          SendSIGHUP = true;
          TimeoutStopSec = "30s";
        };
      };
    })

    (lib.mkIf cfg.greeter.enable {
      # The greeter runs as the greetd user with no $HOME, so its theme config
      # lives system-wide. environment.etc files are world-readable (0444).
      environment.etc."lys-shell/config.yaml" = lib.mkIf (cfg.greeter.settings != {}) {
        source = yamlFormat.generate "lys-greeter-config.yaml" cfg.greeter.settings;
      };

      # Let any local user push their greeter (login-screen) choices from
      # lys-settings to the system config via `pkexec lys-login
      # apply-config` (writes only greeter keys to /etc/lys-shell/runtime.yaml,
      # which the greeter overlays on config.yaml). Admin auth is required.
      security.polkit.enable = true;
      environment.etc."polkit-1/actions/dev.stubbe.lys.greeter.policy".source = "${cfg.package}/share/polkit-1/actions/dev.stubbe.lys.greeter.policy";
    })
  ];
}
