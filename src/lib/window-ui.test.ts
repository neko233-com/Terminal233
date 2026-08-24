import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  applyWindowUiMode,
  getWindowUiMode,
  hasExplicitWindowUiMode,
  isNativeDesktopWindow,
} from "./window-ui";

describe("window UI mode contract", () => {
  beforeEach(() => {
    window.localStorage.clear();
    delete window.__RDESKTOP_WINDOW__;
    window.history.replaceState({}, "", "/");
  });

  it("uses browser mode when the native rdesktop bridge is absent", () => {
    expect(isNativeDesktopWindow()).toBe(false);
    expect(getWindowUiMode()).toBe("browser");
    expect(applyWindowUiMode("system")).toBe(false);
  });

  it("defaults native windows to integrated UI and switches decorations live", () => {
    const setDecorations = vi.fn();
    window.__RDESKTOP_WINDOW__ = {
      minimize: vi.fn(),
      maximize: vi.fn(),
      close: vi.fn(),
      startDrag: vi.fn(),
      setDecorations,
    };

    expect(isNativeDesktopWindow()).toBe(true);
    expect(getWindowUiMode()).toBe("integrated");
    expect(applyWindowUiMode("system")).toBe(true);
    expect(setDecorations).toHaveBeenCalledWith(true);
    expect(getWindowUiMode()).toBe("system");

    expect(applyWindowUiMode("integrated")).toBe(true);
    expect(setDecorations).toHaveBeenLastCalledWith(false);
    expect(getWindowUiMode()).toBe("integrated");
  });

  it("restores a previously selected system window mode for native startup", () => {
    window.localStorage.setItem("terminal233.window-ui-mode", "system");
    window.__RDESKTOP_WINDOW__ = {
      minimize: vi.fn(),
      maximize: vi.fn(),
      close: vi.fn(),
      startDrag: vi.fn(),
      setDecorations: vi.fn(),
    };

    expect(getWindowUiMode()).toBe("system");
  });

  it("honors an explicit native launch mode from the rdesktop URL", () => {
    window.__RDESKTOP_WINDOW__ = {
      minimize: vi.fn(),
      maximize: vi.fn(),
      close: vi.fn(),
      startDrag: vi.fn(),
      setDecorations: vi.fn(),
    };
    window.localStorage.setItem("terminal233.window-ui-mode", "integrated");
    window.history.replaceState({}, "", "/?windowMode=system");

    expect(hasExplicitWindowUiMode()).toBe(true);
    expect(getWindowUiMode()).toBe("system");
    expect(applyWindowUiMode("integrated", false)).toBe(true);
    expect(window.localStorage.getItem("terminal233.window-ui-mode")).toBe(
      "integrated",
    );
  });
});
