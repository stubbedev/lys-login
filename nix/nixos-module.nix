self: {
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.programs.lys-login;
  yamlFormat = pkgs.formats.yaml {};

  # Directory the greeter remembers the last-selected session in. Must be
  # writable by the greeter's user; the tmpfiles rules below create it.
  greeterStateDir = "/var/lib/lys-greeter";

  softwareRenderEnv = import ./software-render-env.nix;

  greeterCompositor =
    if cfg.greeter.renderer == "software"
    then
      pkgs.runCommand "lys-wm-software" {nativeBuildInputs = [pkgs.makeBinaryWrapper];} ''
        makeBinaryWrapper ${lib.getExe' cfg.greeter.compositorPackage "lys-wm"} $out/bin/lys-wm ${
          lib.concatStringsSep " " (lib.mapAttrsToList (k: v: "--set ${k} ${lib.escapeShellArg v}") softwareRenderEnv.lys-wm)
        }
      ''
    else cfg.greeter.compositorPackage;

  # cage draws its own cursor until the greeter window maps (and wlroots scales
  # it per output). Give both cage and the greeter the same cursor as the
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

  sessionDirFlags = flag: none: dirs:
    if dirs == []
    then [none]
    else
      lib.concatMap (d: [
        flag
        d
      ])
      dirs;
  sessionDirArgs =
    sessionDirFlags "--sessions" "--no-sessions" cfg.greeter.session.dirs
    ++ sessionDirFlags "--xsessions" "--no-xsessions" cfg.greeter.session.x11Dirs;

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
        cursorEnv ++ lib.optionals (cfg.greeter.renderer == "software") (lib.mapAttrsToList (k: v: "${k}=${v}") softwareRenderEnv.cage)
      )
      + " ";
  in
    "${wrapperPrefix}${envPrefix}${cageExe} -s -- "
    + "${greeterExe} greet --config /etc/lys-shell/config.yaml "
    + "--state ${greeterStateDir}/last-session ${lib.escapeShellArgs sessionDirArgs} ${envArgs} ${fallback}";

  daemonArgs =
    [
      "--vt"
      (toString cfg.greeter.vt)
      "--service"
      "lys-login"
      "--greeter-user"
      "lys-greeter"
      "--compositor"
      (lib.getExe' greeterCompositor "lys-wm")
    ]
    ++ lib.optionals (cfg.greeter.splashPackage != null) [
      "--splash"
      (lib.getExe' cfg.greeter.splashPackage "lys-splash")
    ]
    ++ lib.optionals (cfg.autologin.user != null) ["--autologin" cfg.autologin.user]
    ++ lib.optionals (cfg.autologin.session != null) ["--autologin-session" cfg.autologin.session]
    ++ [
      "--"
      "--config"
      "/etc/lys-shell/config.yaml"
      "--state"
      "${greeterStateDir}/last-session"
    ]
    ++ sessionDirArgs
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
    ];

  upgradeFailedUnit = "lys-login-upgrade-failed.service";

  upgradeScript = pkgs.writeShellScript "lys-login-upgrade" ''
    if said=$(${lib.escapeShellArgs ([(lib.getExe' cfg.package "lys-login") "upgrade" "--"] ++ daemonArgs)} 2>&1); then
      printf '%s\n' "$said"
      exit 0
    fi
    printf '%s\n' "$said" >&2
    if ${config.systemd.package}/bin/systemctl --quiet is-failed ${upgradeFailedUnit}; then
      ${config.systemd.package}/bin/systemctl reset-failed ${upgradeFailedUnit}
    fi
    ${config.systemd.package}/bin/systemd-run --quiet --wait --unit=${upgradeFailedUnit} \
      --description="lys-login's in-place upgrade failed; the old display manager keeps running" \
      ${pkgs.runtimeShell} -c 'printf "%s\n" "$1" >&2; exit 1' lys-login-upgrade "$said"
    exit 1
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

    autologin = {
      user = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          Account the display manager logs in once per boot, before the first
          greeter, through the `lys-login-autologin` PAM service, which asks
          nothing. With full-disk encryption the passphrase typed at boot opens
          the login keyring when it is the keyring's password
          (`pam_systemd_loadkey`). After that user logs out the greeter shows.
        '';
      };
      session = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Session id the autologin starts (a session file's name, `-x11` for X11 ones); null takes the last session, else the first offered.";
      };
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
        theme and lets the user pick a Wayland session, shown by
        `greeter.displayManager`; on login it starts the selected session
        (discovered from `greeter.session.dirs`)'';

      displayManager = lib.mkOption {
        type = lib.types.enum [
          "greetd"
          "lys-login"
        ];
        default = "lys-login";
        description = ''
          What runs the greeter. `lys-login` is lys-login's own display
          manager: a root daemon on `vt` that shows the greeter in a lys-wm kiosk (`compositorPackage`)
          and starts the session a user logs in to through PAM (the
          `lys-login` service), with neither greetd nor cage. `greetd` runs
          it under greetd in a cage kiosk.
        '';
      };

      compositorPackage = lib.mkOption {
        type = lib.types.package;
        default = self.inputs.lys-wm.packages.${pkgs.stdenv.hostPlatform.system}.default;
        defaultText = lib.literalExpression "lys-login.inputs.lys-wm.packages.\${system}.default";
        example = lib.literalExpression "config.programs.lys-wm.package";
        description = "lys-wm, the kiosk compositor the `lys-login` display manager shows the greeter in.";
      };

      splashPackage = lib.mkOption {
        type = lib.types.nullOr lib.types.package;
        default = null;
        example = lib.literalExpression "lys-splash.packages.\${system}.default";
        description = ''
          lys-splash, when it is the boot splash: the `lys-login` display
          manager takes the screen from it without a blank frame. Its unit
          conflicts with `lys-splash-quit.service`, deactivates the splash
          before lys-wm starts, and quits it with `--retain-splash` once the
          greeter's first frame is presented.
        '';
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
          Render path for the greeter and its compositor.

          `auto` sets nothing. Under the `lys-login` display manager lys-wm
          renders with Vulkan, so on a card without a Vulkan driver (a VM's
          virtio-gpu, say) it stops. Under greetd wlroots (cage) uses its GLES2
          renderer, which needs a GPU driver. It refuses Mesa's software driver,
          so on a host without one cage exits and greetd keeps restarting it.
          `software` needs no GPU vendor driver at all: the greeter draws on
          the CPU (`LYS_UI_RENDERER=cpu`), and its compositor renders with
          GLES on dumb buffers under the daemon (`LYS_WM_RENDERER=gles`, set by
          wrapping `compositorPackage`) or with pixman under greetd
          (`WLR_RENDERER=pixman`). It reaches the greeter's compositor only, not
          the sessions it starts. Use `software` on VMs, headless-ish boxes, or
          any host where GPU init is unreliable and you would rather guarantee
          the login screen renders.
        '';
      };

      graphicsWrapper = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [];
        example = lib.literalExpression ''[ "''${pkgs.nixgl.nixGLIntel}/bin/nixGLIntel" ]'';
        description = ''
          Command prefix wrapping the whole greeter launch (cage + greeter)
          under greetd; the `lys-login` display manager takes none. Unused on
          NixOS, where drivers come from {file}`/run/opengl-driver`.
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
          example = ["LANG=C.UTF-8"];
          description = "Extra `KEY=value` environment entries for every session. `XDG_SESSION_TYPE` is set from the session's kind already; setting it here overrides that for X11 sessions too.";
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
          assertion = cfg.greeter.graphicsWrapper == [];
          message = ''programs.lys-login.greeter.graphicsWrapper wraps the greetd launch of cage; the "lys-login" display manager does not run it. Leave graphicsWrapper empty.'';
        }
      ];
      services.displayManager.enable = true;
      users.users.lys-greeter = {
        isSystemUser = true;
        group = "lys-greeter";
        home = greeterStateDir;
      };
      users.groups.lys-greeter = {};
      services.gnome.at-spi2-core.enable = lib.mkDefault true;
      # One PAM service for both: the greeter's session (class greeter, no
      # authentication) and each login. startSession adds pam_systemd, which
      # registers both with logind on the seat and VT the daemon hands it.
      security.pam.services.lys-login.startSession = true;
      security.polkit.enable = lib.mkDefault true;
      security.pam.services.lys-login-autologin.text = ''
        auth     requisite pam_nologin.so
        auth     [success=ok default=ignore] ${config.systemd.package}/lib/security/pam_systemd_loadkey.so
        auth     required  pam_permit.so
        account  include   lys-login
        password include   lys-login
        session  include   lys-login
      '';
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
        ]
        ++ lib.optionals (cfg.greeter.splashPackage != null) [
          "lys-splash-start.service"
          "lys-splash-quit.service"
        ];
        conflicts =
          ["getty@tty${toString cfg.greeter.vt}.service"]
          ++ lib.optional (cfg.greeter.splashPackage != null) "lys-splash-quit.service";
        restartIfChanged = false;
        reloadIfChanged = true;
        serviceConfig = {
          ExecStartPre = lib.mkIf (cfg.greeter.splashPackage != null) "-${lib.getExe' cfg.greeter.splashPackage "lys-splash"} deactivate";
          ExecStart = lib.escapeShellArgs ([(lib.getExe' cfg.package "lys-login") "daemon"] ++ daemonArgs);
          ExecReload = upgradeScript;
          Restart = "always";
          RestartSec = 1;
          Type = "idle";
          KeyringMode = "inherit";
          IgnoreSIGPIPE = false;
          SendSIGHUP = true;
          TimeoutStopSec = "30s";
        };
      };
    })

    (lib.mkIf cfg.greeter.enable {
      assertions = [
        {
          assertion = cfg.greeter.session.dirs != [] || cfg.greeter.session.x11Dirs != [] || cfg.greeter.session.command != "";
          message = "programs.lys-login.greeter offers no session to log into: session.dirs and session.x11Dirs are empty and session.command is unset. Keep the default dirs, list a directory of session files, or set session.command.";
        }
        {
          assertion = cfg.autologin.user == null || cfg.greeter.displayManager == "lys-login";
          message = "programs.lys-login.autologin needs greeter.displayManager = \"lys-login\": under greetd, set greetd's initial_session instead.";
        }
      ];
      # The greeter runs as a system user with no usable $HOME, so its theme config
      # lives system-wide. environment.etc files are world-readable (0444).
      environment.etc."lys-shell/config.yaml" = lib.mkIf (cfg.greeter.settings != {}) {
        source = yamlFormat.generate "lys-greeter-config.yaml" cfg.greeter.settings;
      };
    })
  ];
}
