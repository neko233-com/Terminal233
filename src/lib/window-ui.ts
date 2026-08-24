export type WindowUiMode = "browser" | "integrated" | "system";

type RdesktopWindowBridge = {
  minimize: () => void;
  maximize: () => void;
  close: () => void;
  startDrag: () => void;
  setDecorations?: (decorations: boolean) => void;
};

declare global {
  interface Window {
    __RDESKTOP_WINDOW__?: RdesktopWindowBridge;
  }
}

const WINDOW_UI_MODE_KEY = "terminal233.window-ui-mode";

function nativeWindowBridge(): RdesktopWindowBridge | null {
  if (typeof window === "undefined") return null;
  const bridge = window.__RDESKTOP_WINDOW__;
  return bridge?.setDecorations ? bridge : null;
}

export function isNativeDesktopWindow(): boolean {
  return nativeWindowBridge() !== null;
}

export function isBrowserDevelopmentMode(): boolean {
  return import.meta.env.MODE === "browser";
}

export function hasExplicitWindowUiMode(): boolean {
  const requestedMode = new URLSearchParams(window.location.search).get(
    "windowMode",
  );
  return requestedMode === "system" || requestedMode === "integrated";
}

export function getWindowUiMode(): WindowUiMode {
  if (!isNativeDesktopWindow()) return "browser";
  if (isBrowserDevelopmentMode()) return "browser";
  if (hasExplicitWindowUiMode()) {
    return new URLSearchParams(window.location.search).get(
      "windowMode",
    ) as "system" | "integrated";
  }
  try {
    return window.localStorage.getItem(WINDOW_UI_MODE_KEY) === "system"
      ? "system"
      : "integrated";
  } catch {
    return "integrated";
  }
}

export function applyWindowUiMode(
  mode: Exclude<WindowUiMode, "browser">,
  persist = true,
): boolean {
  const bridge = nativeWindowBridge();
  if (!bridge) return false;
  bridge.setDecorations?.(mode === "system");
  if (persist) {
    try {
      window.localStorage.setItem(WINDOW_UI_MODE_KEY, mode);
    } catch {
      // A restricted WebView profile may deny localStorage. The live switch still applies.
    }
  }
  window.dispatchEvent(
    new CustomEvent<WindowUiMode>("terminal233-window-ui-mode", {
      detail: mode,
    }),
  );
  return true;
}

export function startNativeWindowDrag(): void {
  nativeWindowBridge()?.startDrag();
}

export function minimizeNativeWindow(): void {
  nativeWindowBridge()?.minimize();
}

export function maximizeNativeWindow(): void {
  nativeWindowBridge()?.maximize();
}

export function closeNativeWindow(): void {
  nativeWindowBridge()?.close();
}
