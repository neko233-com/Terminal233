import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WindowUiControl } from "./WindowUiControl";

describe("WindowUiControl", () => {
  beforeEach(() => {
    window.localStorage.clear();
    window.history.replaceState({}, "", "/");
    delete window.__RDESKTOP_WINDOW__;
  });

  it("sends live decoration changes through the native rdesktop bridge", async () => {
    const setDecorations = vi.fn();
    window.__RDESKTOP_WINDOW__ = {
      minimize: vi.fn(),
      maximize: vi.fn(),
      close: vi.fn(),
      startDrag: vi.fn(),
      setDecorations,
    };
    const user = userEvent.setup();

    render(<WindowUiControl />);
    const selector = screen.getByRole("combobox", { name: "切换界面模式" });
    expect(screen.getByText("一体化 UI", { selector: "span" })).toBeInTheDocument();

    await user.selectOptions(selector, "system");
    expect(setDecorations).toHaveBeenLastCalledWith(true);
    expect(screen.getByText("系统窗口 UI", { selector: "span" })).toBeInTheDocument();

    await user.selectOptions(selector, "integrated");
    expect(setDecorations).toHaveBeenLastCalledWith(false);
    expect(screen.getByText("一体化 UI", { selector: "span" })).toBeInTheDocument();
  });
});
