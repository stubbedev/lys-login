# The variables that force each render layer onto its driver-free path:
# lys-ui's CPU renderer (shared memory, no Vulkan) for the greeter client and,
# for its compositor, wlroots' pixman renderer (DRM dumb buffers, no EGL/GBM)
# for cage or lys-wm's GLES renderer (llvmpipe on dumb buffers, no Vulkan).
# None needs a GPU vendor driver, so the greeter always renders — on a VM, a
# driverless box, or a non-NixOS install without nixGL.
let
  greeter = {LYS_UI_RENDERER = "cpu";};
in {
  cage =
    greeter
    // {
      WLR_RENDERER = "pixman";
      WLR_DRM_NO_MODIFIERS = "1";
    };
  lys-wm = greeter // {LYS_WM_RENDERER = "gles";};
}
