import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import Terminal233IdeApp from "./Terminal233IdeApp";

const ide = vi.hoisted(() => ({
  acknowledgeIdeDownloadChunk: vi.fn(),
  acknowledgeIdeTerminalOutput: vi.fn(),
  cancelIdeTransfer: vi.fn(),
  closeIdeTerminal: vi.fn(),
  createIdeConnection: vi.fn(),
  createIdeDirectory: vi.fn(),
  deleteIdeRemote: vi.fn(),
  disconnectIdeConnection: vi.fn(),
  downloadIdeFiles: vi.fn(),
  listIdeConnections: vi.fn(),
  listIdeDirectory: vi.fn(),
  onIdeTransferProgress: vi.fn(),
  prepareIdeConnection: vi.fn(),
  rejectIdeHostKey: vi.fn(),
  renameIdeRemote: vi.fn(),
  resizeIdeTerminal: vi.fn(),
  startIdeTerminal: vi.fn(),
  trustIdeHostKey: vi.fn(),
  uploadIdeFiles: vi.fn(),
  writeIdeTerminal: vi.fn(),
  IDE_RUNTIME_LABEL: "纯 Rust puressh",
  IDE_SECURITY_NOTE: "SSH 与 SFTP 均由纯 Rust puressh 进程内执行。",
  IDE_TRANSPORT_LABEL: "进程内 TCP",
}));

const xterm = vi.hoisted(() => ({
  instances: [] as Array<{ options: Record<string, unknown> }>,
}));

vi.mock("./lib/ide-runtime", () => ({
  ...ide,
  decodeBase64Bytes: (value: string) =>
    Uint8Array.from(atob(value), (character) => character.charCodeAt(0)),
  ideError: (error: unknown) =>
    error instanceof Error
      ? error.message
      : ((error as { message?: string } | null)?.message ?? "操作失败。"),
  IdeTerminalInputQueue: class {
    enqueue() {
      return true;
    }

    close() {}
  },
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {}
  },
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 110;
    rows = 34;
    options: Record<string, unknown>;

    constructor(options: Record<string, unknown>) {
      this.options = { ...options };
      xterm.instances.push(this);
    }

    loadAddon() {}

    open() {}

    write(_data: unknown, callback?: () => void) {
      callback?.();
    }

    focus() {}

    dispose() {}

    onData() {
      return { dispose() {} };
    }
  },
}));

class TestResizeObserver {
  observe() {}

  disconnect() {}
}

const connection = {
  id: "connection-1",
  label: "测试服务器",
  host: "example.test",
  port: 22,
  username: "root",
  tags: ["个人服务器"],
  expiresInSeconds: 1_800,
};

const trustedHost = {
  status: "trusted" as const,
  host: connection.host,
  port: connection.port,
  keyAlgorithm: "ssh-ed25519",
  fingerprint: "SHA256:trusted-host",
};

function deferred<Value>() {
  let resolve!: (value: Value) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<Value>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

async function openCreateDialog(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByText("还没有 SSH 连接");
  await user.click(screen.getAllByRole("button", { name: "新建连接" })[0]);
  return screen.getByRole("dialog", { name: "新建临时 SSH 连接" });
}

async function openWorkspace(user: ReturnType<typeof userEvent.setup>) {
  const row = await screen.findByRole("row", { name: /测试服务器/ });
  await user.click(within(row).getByRole("button", { name: "连接" }));
  return screen.findByRole("region", { name: "交互式 SSH 终端" });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal("ResizeObserver", TestResizeObserver);
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    callback(0);
    return 1;
  });
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => ({
      matches: false,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  );
  xterm.instances.length = 0;

  ide.listIdeConnections.mockResolvedValue([]);
  ide.disconnectIdeConnection.mockResolvedValue(undefined);
  ide.prepareIdeConnection.mockResolvedValue(trustedHost);
  ide.rejectIdeHostKey.mockResolvedValue(undefined);
  ide.trustIdeHostKey.mockResolvedValue(trustedHost);
  ide.listIdeDirectory.mockResolvedValue({
    path: "/",
    entries: [],
    truncated: false,
  });
  ide.onIdeTransferProgress.mockResolvedValue(vi.fn());
  ide.startIdeTerminal.mockResolvedValue({ sessionId: "terminal-1" });
  ide.acknowledgeIdeTerminalOutput.mockResolvedValue(undefined);
  ide.resizeIdeTerminal.mockResolvedValue(undefined);
  ide.closeIdeTerminal.mockResolvedValue(undefined);
  ide.cancelIdeTransfer.mockResolvedValue(undefined);
  ide.createIdeDirectory.mockResolvedValue(undefined);
  ide.renameIdeRemote.mockResolvedValue(undefined);
  ide.deleteIdeRemote.mockResolvedValue(undefined);
});

describe("Terminal233 rdesktop SSH IDE", () => {
  it("prevents the WebView from persisting connection credentials through autofill", async () => {
    const user = userEvent.setup();
    render(<Terminal233IdeApp />);
    const dialog = await openCreateDialog(user);

    expect(dialog).toHaveAttribute("autocomplete", "off");
    expect(within(dialog).getByLabelText("用户")).toHaveAttribute(
      "autocomplete",
      "off",
    );
    expect(within(dialog).getByLabelText("密码")).toHaveAttribute(
      "autocomplete",
      "new-password",
    );
    const authenticationSelect = within(dialog).getByRole("combobox", {
      name: "认证方式",
    });
    expect(
      within(authenticationSelect)
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual(["密码", "粘贴私钥"]);
    expect(
      within(dialog).queryByRole("option", { name: "SSH Agent" }),
    ).not.toBeInTheDocument();

    await user.selectOptions(authenticationSelect, "private_key");
    expect(within(dialog).getByLabelText("私钥")).toHaveAttribute(
      "autocomplete",
      "off",
    );
    expect(within(dialog).getByLabelText("私钥口令（可选）")).toHaveAttribute(
      "autocomplete",
      "new-password",
    );
  });

  it("rejects an out-of-range port without creating a connection", async () => {
    const user = userEvent.setup();
    render(<Terminal233IdeApp />);
    const dialog = await openCreateDialog(user);

    await user.type(within(dialog).getByLabelText("主机"), "example.test");
    await user.type(within(dialog).getByLabelText("密码"), "secret");
    const port = within(dialog).getByLabelText("端口");
    await user.clear(port);
    await user.type(port, "65536");
    await user.click(within(dialog).getByRole("button", { name: "连接" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "端口必须是 1 到 65535 之间的整数。",
    );
    expect(ide.createIdeConnection).not.toHaveBeenCalled();
  });

  it("discards a late prepare result after closing progress and rejects its confirmation", async () => {
    const user = userEvent.setup();
    const prepare = deferred<{
      status: "confirmation_required";
      confirmationId: string;
      host: string;
      port: number;
      keyAlgorithm: string;
      fingerprint: string;
    }>();
    ide.listIdeConnections.mockResolvedValue([connection]);
    ide.prepareIdeConnection.mockReturnValue(prepare.promise);

    render(<Terminal233IdeApp />);
    const row = await screen.findByRole("row", { name: /测试服务器/ });
    await user.click(within(row).getByRole("button", { name: "连接" }));
    const progress = await screen.findByRole("dialog", { name: "SSH 连接" });
    const closeButtons = within(progress).getAllByRole("button", {
      name: "关闭",
    });
    await user.click(closeButtons[closeButtons.length - 1]);
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "SSH 连接" }),
      ).not.toBeInTheDocument(),
    );

    await act(async () => {
      prepare.resolve({
        status: "confirmation_required",
        confirmationId: "confirmation-late",
        host: connection.host,
        port: connection.port,
        keyAlgorithm: "ssh-ed25519",
        fingerprint: "SHA256:late-host",
      });
      await prepare.promise;
    });

    await waitFor(() =>
      expect(ide.rejectIdeHostKey).toHaveBeenCalledWith("confirmation-late"),
    );
    expect(
      screen.queryByRole("region", { name: "交互式 SSH 终端" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("row", { name: /测试服务器/ })).toBeVisible();
  });

  it("keeps credential-probe failures in the Chinese connection progress dialog", async () => {
    const user = userEvent.setup();
    ide.listIdeConnections.mockResolvedValue([connection]);
    ide.prepareIdeConnection.mockRejectedValue({
      code: "ssh_authentication_failed",
      message: "SSH 认证失败，请检查用户名和凭据。",
    });

    render(<Terminal233IdeApp />);
    const row = await screen.findByRole("row", { name: /测试服务器/ });
    await user.click(within(row).getByRole("button", { name: "连接" }));

    const progress = await screen.findByRole("dialog", { name: "SSH 连接" });
    expect(within(progress).getByRole("alert")).toHaveTextContent(
      "SSH 认证失败，请检查用户名和凭据。",
    );
    expect(
      within(progress).getByRole("button", { name: "重试" }),
    ).toBeVisible();
    expect(
      screen.queryByRole("region", { name: "交互式 SSH 终端" }),
    ).not.toBeInTheDocument();
  });

  it("reorders connections through the HTML5 drag-and-drop path", async () => {
    const secondConnection = {
      ...connection,
      id: "connection-2",
      label: "备用服务器",
      host: "backup.example.test",
    };
    ide.listIdeConnections.mockResolvedValue([connection, secondConnection]);

    render(<Terminal233IdeApp />);
    const firstRow = await screen.findByRole("row", { name: /测试服务器/ });
    const secondRow = screen.getByRole("row", { name: /备用服务器/ });

    fireEvent.dragStart(firstRow);
    fireEvent.dragOver(secondRow);
    fireEvent.drop(secondRow);
    fireEvent.dragEnd(firstRow);

    await waitFor(() => {
      const rows = screen.getAllByRole("row");
      expect(rows.indexOf(secondRow)).toBeLessThan(rows.indexOf(firstRow));
    });
  });

  it("renders the dense workstation chrome and filters the open session tree", async () => {
    const user = userEvent.setup();
    ide.listIdeConnections.mockResolvedValue([connection]);

    render(<Terminal233IdeApp />);
    await openWorkspace(user);

    expect(
      screen.getByRole("navigation", { name: "工作台工具栏" }),
    ).toBeVisible();
    expect(screen.getByRole("tablist", { name: "已打开会话" })).toBeVisible();
    expect(screen.getByRole("tab", { name: /测试服务器/ })).toBeVisible();
    expect(screen.getByText("会话树")).toBeVisible();
    const toolsTab = screen.getByRole("button", { name: "工具" });
    await user.click(toolsTab);
    expect(toolsTab).toHaveClass("active");

    const filter = screen.getByLabelText("筛选已打开会话");
    await user.type(filter, "不存在");
    expect(screen.queryByRole("button", { name: /测试服务器.*example\.test/ })).not.toBeInTheDocument();
  });

  it("closes the active session tab without deleting the saved connection", async () => {
    const user = userEvent.setup();
    ide.listIdeConnections.mockResolvedValue([connection]);

    render(<Terminal233IdeApp />);
    await openWorkspace(user);
    await user.click(screen.getByRole("button", { name: "关闭 测试服务器" }));

    expect(
      screen.queryByRole("region", { name: "交互式 SSH 终端" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("row", { name: /测试服务器/ })).toBeVisible();
  });

  it("moves by the adjacent visible connection while a group is filtered", async () => {
    const user = userEvent.setup();
    const hiddenConnection = {
      ...connection,
      id: "connection-hidden",
      label: "隐藏服务器",
      host: "hidden.example.test",
      tags: ["其他分组"],
    };
    const visibleConnection = {
      ...connection,
      id: "connection-visible-2",
      label: "可见服务器二",
      host: "visible-2.example.test",
    };
    ide.listIdeConnections.mockResolvedValue([
      connection,
      hiddenConnection,
      visibleConnection,
    ]);

    render(<Terminal233IdeApp />);
    await screen.findByRole("row", { name: /测试服务器/ });
    await user.click(screen.getByRole("button", { name: /个人服务器/ }));
    const firstVisible = screen.getByRole("row", { name: /测试服务器/ });
    const secondVisible = screen.getByRole("row", { name: /可见服务器二/ });

    fireEvent.keyDown(firstVisible, { key: "ArrowDown", shiftKey: true });

    await waitFor(() => {
      const rows = screen.getAllByRole("row");
      expect(rows.indexOf(secondVisible)).toBeLessThan(
        rows.indexOf(firstVisible),
      );
    });
    expect(
      screen.queryByRole("row", { name: /隐藏服务器/ }),
    ).not.toBeInTheDocument();
  });

  it("starts an SFTP upload when files are dropped onto the remote file pane", async () => {
    const user = userEvent.setup();
    const file = new File(["hello"], "report.txt", { type: "text/plain" });
    ide.listIdeConnections.mockResolvedValue([connection]);
    ide.uploadIdeFiles.mockImplementation(
      (
        _connectionId: string,
        _remoteDirectory: string,
        _files: File[],
        onEvent: (event: unknown) => void,
      ) => {
        const started = {
          jobId: "upload-1",
          direction: "upload",
          totalFiles: 1,
          totalBytes: file.size,
          items: [{ fileId: "file-1", name: file.name, size: file.size }],
        };
        onEvent({ kind: "started", ...started });
        return Promise.resolve(started);
      },
    );

    render(<Terminal233IdeApp />);
    await openWorkspace(user);
    const filePane = screen.getByText("拖入文件即可上传").closest("aside");
    if (!filePane) throw new Error("remote file pane not found");

    fireEvent.dragOver(filePane, { dataTransfer: { files: [file] } });
    fireEvent.drop(filePane, { dataTransfer: { files: [file] } });

    await waitFor(() =>
      expect(ide.uploadIdeFiles).toHaveBeenCalledWith(
        connection.id,
        "/",
        [file],
        expect.any(Function),
      ),
    );
  });

  it("handles upload completion delivered before the start command promise resolves", async () => {
    const user = userEvent.setup();
    const file = new File(["done"], "fast.txt", { type: "text/plain" });
    ide.listIdeConnections.mockResolvedValue([connection]);
    ide.uploadIdeFiles.mockImplementation(
      (
        _connectionId: string,
        _remoteDirectory: string,
        _files: File[],
        onEvent: (event: unknown) => void,
      ) => {
        const started = {
          jobId: "upload-fast",
          direction: "upload",
          totalFiles: 1,
          totalBytes: file.size,
          items: [{ fileId: "file-fast", name: file.name, size: file.size }],
        };
        onEvent({ kind: "started", ...started });
        onEvent({
          kind: "completed",
          jobId: started.jobId,
          direction: "upload",
          status: "completed",
          completedFiles: 1,
          failedFiles: 0,
          cancelledFiles: 0,
          bytesTransferred: file.size,
          totalBytes: file.size,
        });
        return Promise.resolve(started);
      },
    );

    render(<Terminal233IdeApp />);
    await openWorkspace(user);
    const filePane = screen.getByText("拖入文件即可上传").closest("aside");
    if (!filePane) throw new Error("remote file pane not found");

    fireEvent.drop(filePane, { dataTransfer: { files: [file] } });

    expect(await screen.findByText("上传完成：1 个文件。")).toBeVisible();
    await waitFor(() => expect(ide.listIdeDirectory).toHaveBeenCalledTimes(2));
  });

  it("rejects an upload completion whose status contradicts its counters", async () => {
    const user = userEvent.setup();
    const file = new File(["bad"], "bad-result.txt", { type: "text/plain" });
    ide.listIdeConnections.mockResolvedValue([connection]);
    ide.uploadIdeFiles.mockImplementation(
      (
        _connectionId: string,
        _remoteDirectory: string,
        _files: File[],
        onEvent: (event: unknown) => void,
      ) => {
        const started = {
          jobId: "upload-bad-result",
          direction: "upload",
          totalFiles: 1,
          totalBytes: file.size,
          items: [
            { fileId: "file-bad-result", name: file.name, size: file.size },
          ],
        };
        onEvent({ kind: "started", ...started });
        onEvent({
          kind: "completed",
          jobId: started.jobId,
          direction: "upload",
          status: "completed_with_errors",
          completedFiles: 1,
          failedFiles: 0,
          cancelledFiles: 0,
          bytesTransferred: file.size,
          totalBytes: file.size,
        });
        return new Promise(() => undefined);
      },
    );

    render(<Terminal233IdeApp />);
    await openWorkspace(user);
    const filePane = screen.getByText("拖入文件即可上传").closest("aside");
    if (!filePane) throw new Error("remote file pane not found");
    fireEvent.drop(filePane, { dataTransfer: { files: [file] } });

    expect(
      await screen.findByText("上传结果校验失败，已取消传输。"),
    ).toBeVisible();
    await waitFor(() =>
      expect(ide.cancelIdeTransfer).toHaveBeenCalledWith("upload-bad-result"),
    );
  });

  it("rejects upload channel data received after a completed event", async () => {
    const user = userEvent.setup();
    const file = new File(["done"], "late-event.txt", { type: "text/plain" });
    ide.listIdeConnections.mockResolvedValue([connection]);
    ide.uploadIdeFiles.mockImplementation(
      (
        _connectionId: string,
        _remoteDirectory: string,
        _files: File[],
        onEvent: (event: unknown) => void,
      ) => {
        const started = {
          jobId: "upload-late-event",
          direction: "upload",
          totalFiles: 1,
          totalBytes: file.size,
          items: [
            { fileId: "file-late-event", name: file.name, size: file.size },
          ],
        };
        onEvent({ kind: "started", ...started });
        onEvent({
          kind: "completed",
          jobId: started.jobId,
          direction: "upload",
          status: "completed",
          completedFiles: 1,
          failedFiles: 0,
          cancelledFiles: 0,
          bytesTransferred: file.size,
          totalBytes: file.size,
        });
        onEvent({
          kind: "progress",
          jobId: started.jobId,
          fileId: "file-late-event",
          name: file.name,
          direction: "upload",
          fileBytesTransferred: file.size,
          fileTotalBytes: file.size,
          jobBytesTransferred: file.size,
          jobTotalBytes: file.size,
        });
        return new Promise(() => undefined);
      },
    );

    render(<Terminal233IdeApp />);
    await openWorkspace(user);
    const filePane = screen.getByText("拖入文件即可上传").closest("aside");
    if (!filePane) throw new Error("remote file pane not found");
    fireEvent.drop(filePane, { dataTransfer: { files: [file] } });

    expect(
      await screen.findByText("上传任务在完成后仍收到数据，已丢弃异常结果。"),
    ).toBeVisible();
    await waitFor(() =>
      expect(ide.cancelIdeTransfer).toHaveBeenCalledWith("upload-late-event"),
    );
  });

  it("disables xterm cursor blinking for reduced motion and announces terminal state changes", async () => {
    const user = userEvent.setup();
    const changeListener: { current: (() => void) | null } = { current: null };
    const mediaQuery = {
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener: vi.fn((_type: string, listener: () => void) => {
        changeListener.current = listener;
      }),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    };
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => mediaQuery),
    );
    ide.listIdeConnections.mockResolvedValue([connection]);

    render(<Terminal233IdeApp />);
    await openWorkspace(user);

    await waitFor(() =>
      expect(
        xterm.instances[xterm.instances.length - 1]?.options.cursorBlink,
      ).toBe(false),
    );
    const terminalState = await screen.findByText("已连接");
    expect(terminalState).toHaveAttribute("role", "status");
    expect(terminalState).toHaveAttribute("aria-live", "polite");

    act(() => {
      mediaQuery.matches = false;
      changeListener.current?.();
    });
    expect(
      xterm.instances[xterm.instances.length - 1]?.options.cursorBlink,
    ).toBe(true);
  });

  it("passes private-key authentication exactly once and clears key material immediately", async () => {
    const user = userEvent.setup();
    const create = deferred<typeof connection>();
    ide.createIdeConnection.mockReturnValue(create.promise);

    render(<Terminal233IdeApp />);
    const dialog = await openCreateDialog(user);
    await user.type(within(dialog).getByLabelText("主机"), "key.example.test");
    await user.clear(within(dialog).getByLabelText("端口"));
    await user.type(within(dialog).getByLabelText("端口"), "2222");
    await user.selectOptions(
      within(dialog).getByRole("combobox", { name: "认证方式" }),
      "private_key",
    );
    const privateKey = within(dialog).getByLabelText("私钥");
    const passphrase = within(dialog).getByLabelText("私钥口令（可选）");
    await user.type(
      privateKey,
      "-----BEGIN OPENSSH PRIVATE KEY-----\\ntest-key",
    );
    await user.type(passphrase, "key-secret");
    await user.click(within(dialog).getByRole("button", { name: "连接" }));

    await waitFor(() =>
      expect(ide.createIdeConnection).toHaveBeenCalledWith({
        label: "key.example.test",
        host: "key.example.test",
        port: 2222,
        username: "root",
        authentication: {
          kind: "private_key",
          privateKeyPem: "-----BEGIN OPENSSH PRIVATE KEY-----\\ntest-key",
          passphrase: "key-secret",
        },
        tags: ["个人服务器"],
        terminal: { columns: 110, rows: 34, command: null },
      }),
    );
    expect(privateKey).toHaveValue("");
    expect(passphrase).toHaveValue("");

    await user.click(within(dialog).getByRole("button", { name: "关闭" }));
    await act(async () => {
      create.resolve(connection);
      await create.promise;
    });
    await waitFor(() =>
      expect(ide.disconnectIdeConnection).toHaveBeenCalledWith(connection.id),
    );
  });

  it("deletes the absolute SFTP path captured when the file menu opens", async () => {
    const user = userEvent.setup();
    ide.listIdeConnections.mockResolvedValue([connection]);
    ide.listIdeDirectory.mockImplementation(
      (_connectionId: string, path: string) =>
        Promise.resolve(
          path === "/archive"
            ? {
                path: "/archive",
                entries: [
                  {
                    name: "report.txt",
                    isDirectory: false,
                    size: 128,
                    modifiedAtUnix: null,
                  },
                ],
                truncated: false,
              }
            : {
                path: "/",
                entries: [
                  {
                    name: "archive",
                    isDirectory: true,
                    size: null,
                    modifiedAtUnix: null,
                  },
                ],
                truncated: false,
              },
        ),
    );

    render(<Terminal233IdeApp />);
    await openWorkspace(user);
    const folder = await screen.findByRole("button", {
      name: /archive，文件夹/,
    });
    await user.dblClick(folder);
    const report = await screen.findByRole("button", {
      name: /report\.txt，文件/,
    });

    fireEvent.contextMenu(report, { clientX: 120, clientY: 80 });
    const menu = await screen.findByRole("menu", { name: "report.txt 操作" });
    await user.click(within(menu).getByRole("menuitem", { name: "删除" }));
    const dialog = await screen.findByRole("dialog", { name: "确认删除" });
    expect(within(dialog).getByText("/archive/report.txt")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "确认删除" }));

    await waitFor(() =>
      expect(ide.deleteIdeRemote).toHaveBeenCalledWith(
        connection.id,
        "/archive/report.txt",
      ),
    );
  });
});
