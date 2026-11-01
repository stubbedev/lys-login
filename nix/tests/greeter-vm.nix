# What a VM needs to show the greeter on a virtual display, and the wait
# for it to take keys, shared by the VM checks of its display managers.
#
# script journal is the Python helper await_ready(machine, field,
# occurrence): it waits for the greeter, whose log the journalctl arguments
# `journal` select, to say what is typed now reaches the form: its first
# frame is shown and the compositor gave it the keyboard, with the named
# field focused, for the occurrence'th time. Keys sent earlier are dropped
# by the compositor, and pixels cannot tell: the kernel's console and an
# empty kiosk are on screen until the form is.
{pkgs}: {
  module = {
    virtualisation.graphics = true;
    virtualisation.cores = 4;
    virtualisation.memorySize = 2048;
    virtualisation.qemu.options = [
      "-vga none"
      "-device virtio-gpu-pci"
    ];
    environment.sessionVariables.LIBGL_ALWAYS_SOFTWARE = "1";
    hardware.graphics.enable = true;
    hardware.graphics.extraPackages = [pkgs.mesa];
    fonts.packages = [pkgs.inter];
    systemd.tmpfiles.rules = ["d /tmp/lys-login 0777 root root -"];
  };

  script = journal: ''
    def await_ready(machine, field, occurrence):
        machine.wait_until_succeeds(
            f"[ \"$(journalctl ${journal} -o cat --no-pager | grep -c 'greeter: ready: .* the {field} field')\" -ge {occurrence} ]",
            timeout=180,
        )
  '';
}
