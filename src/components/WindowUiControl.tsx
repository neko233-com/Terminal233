import { Minus, Square, X } from "lucide-react";
import {
  applyWindowUiMode,
  closeNativeWindow,
  hasExplicitWindowUiMode,
  getWindowUiMode,
  isNativeDesktopWindow,
  maximizeNativeWindow,
  minimizeNativeWindow,
  type WindowUiMode,
} from "../lib/window-ui";
import { useEffect, useState } from "react";

const MODE_LABELS: Record<WindowUiMode, string> = {
  browser: "浏览器调试",
  integrated: "一体化 UI",
  system: "系统窗口 UI",
};

export function WindowUiControl() {
  const [mode, setMode] = useState<WindowUiMode>(() => getWindowUiMode());
  const native = isNativeDesktopWindow();

  useEffect(() => {
    if (!native) return;
    const current = getWindowUiMode();
    setMode(current);
    if (current !== "browser")
      applyWindowUiMode(current, hasExplicitWindowUiMode() && current === "integrated");
  }, [native]);

  useEffect(() => {
    const handleModeChange = (event: Event) => {
      const next = (event as CustomEvent<WindowUiMode>).detail;
      if (next === "integrated" || next === "system") setMode(next);
    };
    window.addEventListener("terminal233-window-ui-mode", handleModeChange);
    return () =>
      window.removeEventListener("terminal233-window-ui-mode", handleModeChange);
  }, []);

  function changeMode(next: WindowUiMode) {
    if (next === "browser") return;
    if (applyWindowUiMode(next)) setMode(next);
  }

  return (
    <div className="ide-window-ui-control" aria-label="界面模式">
      <span className="ide-window-ui-label">{MODE_LABELS[mode]}</span>
      {native ? (
        <select
          aria-label="切换界面模式"
          value={mode}
          onChange={(event) => changeMode(event.target.value as WindowUiMode)}
        >
          <option value="integrated">一体化 UI</option>
          <option value="system">系统窗口 UI</option>
        </select>
      ) : null}
    </div>
  );
}

export function NativeWindowControls() {
  const [mode, setMode] = useState<WindowUiMode>(() => getWindowUiMode());

  useEffect(() => {
    const handleModeChange = (event: Event) => {
      const next = (event as CustomEvent<WindowUiMode>).detail;
      if (next === "integrated" || next === "system") setMode(next);
    };
    window.addEventListener("terminal233-window-ui-mode", handleModeChange);
    return () =>
      window.removeEventListener("terminal233-window-ui-mode", handleModeChange);
  }, []);

  if (!isNativeDesktopWindow() || mode !== "integrated") return null;
  return (
    <div className="ide-native-window-controls" aria-label="窗口控制">
      <button aria-label="最小化窗口" onClick={minimizeNativeWindow} type="button">
        <Minus size={14} />
      </button>
      <button aria-label="最大化或还原窗口" onClick={maximizeNativeWindow} type="button">
        <Square size={12} />
      </button>
      <button aria-label="关闭窗口" onClick={closeNativeWindow} type="button">
        <X size={14} />
      </button>
    </div>
  );
}
