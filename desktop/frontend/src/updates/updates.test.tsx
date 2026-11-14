import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it } from "vitest";
import { App } from "../App";
import { fakeBridge } from "../api/fakeBridge";
import type { HostAPI, UpdateReply } from "../api/types";

const available: UpdateReply = {
  state: "available",
  current: "0.2.0",
  latest: "0.3.0",
  releaseURL: "https://github.com/moogie-labs/recall/releases/tag/v0.3.0",
  problem: null,
};

function eventHost(overrides: Partial<HostAPI> = {}) {
  const handlers = new Map<string, (payload: any) => void>();
  const host: HostAPI = {
    ready: async () => {},
    completeQuit: async () => ({ problem: null }),
    openExternal: async () => ({ problem: null }),
    on: (name, callback) => {
      handlers.set(name, callback);
      return () => handlers.delete(name);
    },
    ...overrides,
  };
  const emit = (name: string, payload: unknown) =>
    act(() => handlers.get(name)!(payload));
  return { host, emit };
}

it("offers an available upgrade on launch and keeps it in the footer after Not now", async () => {
  const api = fakeBridge();
  const checks: boolean[] = [];
  api.checkForUpdate = async (manual) => {
    checks.push(manual);
    return available;
  };
  const { host } = eventHost();
  render(<App api={api} host={host} />);
  expect(
    await screen.findByText("Recall 0.3.0 is available. You have 0.2.0."),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Not now" }));
  await waitFor(() =>
    expect(
      screen.queryByText("Recall 0.3.0 is available. You have 0.2.0."),
    ).not.toBeInTheDocument(),
  );
  expect(screen.getByText("Recall 0.2.0")).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Upgrade available" }),
  ).toBeInTheDocument();
  expect(checks).toEqual([false]);
});

it("opens release notes from the upgrade prompt", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  const opened: string[] = [];
  const { host } = eventHost({
    openExternal: async (url) => {
      opened.push(url);
      return { problem: null };
    },
  });
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Release notes" }));
  expect(opened).toEqual([available.releaseURL]);
});

it("shows upgrade progress until Installer opens", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  let finish!: (reply: { problem: null }) => void;
  let starts = 0;
  api.startUpgrade = () => {
    starts++;
    return new Promise((resolve) => {
      finish = resolve;
    });
  };
  const { host, emit } = eventHost();
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Upgrade" }));
  expect(starts).toBe(1);
  emit("desktop:upgrade-progress", {
    stage: "downloading",
    done: 50,
    total: 100,
  });
  expect(screen.getByText("Downloading Recall 0.3.0… 50%")).toBeInTheDocument();
  emit("desktop:upgrade-progress", {
    stage: "verifying",
    done: 100,
    total: 100,
  });
  expect(screen.getByText("Verifying…")).toBeInTheDocument();
  await act(async () => finish({ problem: null }));
  expect(screen.getByText("Opening Installer…")).toBeInTheDocument();
});

it("reports a failed upgrade and offers a retry", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  let starts = 0;
  api.startUpgrade = async () => {
    starts++;
    if (starts === 1)
      return {
        problem: {
          code: "verification_failed",
          message: "Gatekeeper rejected the downloaded installer",
          path: "",
        },
      };
    return new Promise(() => {});
  };
  const { host } = eventHost();
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Not now" }));
  fireEvent.click(screen.getByRole("button", { name: "Upgrade available" }));
  expect(
    await screen.findByText("Gatekeeper rejected the downloaded installer"),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Retry upgrade" }));
  expect(starts).toBe(2);
  expect(screen.getByText("Downloading Recall 0.3.0…")).toBeInTheDocument();
});

const cancelled = {
  problem: {
    code: "conflict",
    message: "Upgrade cancelled: unsaved changes could not be saved.",
    path: "",
  },
};

it("keeps the cancel message when the OK reply arrives after the cancel event", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  let finish!: (reply: { problem: null }) => void;
  api.startUpgrade = () =>
    new Promise((resolve) => {
      finish = resolve;
    });
  const { host, emit } = eventHost();
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Upgrade" }));
  emit("desktop:upgrade-cancelled", cancelled);
  await act(async () => finish({ problem: null }));
  expect(screen.getByText(cancelled.problem.message)).toBeInTheDocument();
  expect(screen.queryByText("Opening Installer…")).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Retry upgrade" }),
  ).toBeInTheDocument();
});

it("keeps the error when progress arrives after a failure", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  api.startUpgrade = async () => ({ problem: null });
  const { host, emit } = eventHost();
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Upgrade" }));
  emit("desktop:upgrade-cancelled", cancelled);
  emit("desktop:upgrade-progress", {
    stage: "downloading",
    done: 10,
    total: 100,
  });
  expect(screen.getByText(cancelled.problem.message)).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Retry upgrade" }),
  ).toBeInTheDocument();
});

it("starts the upgrade once when Retry is double-clicked", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  let starts = 0;
  api.startUpgrade = () => {
    starts++;
    return new Promise(() => {});
  };
  const { host, emit } = eventHost();
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Upgrade" }));
  emit("desktop:upgrade-cancelled", cancelled);
  const retry = screen.getByRole("button", { name: "Retry upgrade" });
  fireEvent.click(retry);
  fireEvent.click(retry);
  expect(starts).toBe(2);
});

it("skips the launch check when it is off or in a development build", async () => {
  for (const info of [
    { version: "0.2.0", development: false, checkForUpdates: false },
    { version: "dev", development: true, checkForUpdates: true },
  ]) {
    const api = fakeBridge();
    let checks = 0;
    api.appInfo = async () => info;
    api.checkForUpdate = async () => {
      checks++;
      return available;
    };
    const { host } = eventHost();
    const { unmount } = render(<App api={api} host={host} />);
    expect(
      await screen.findByText("Recall " + info.version),
    ).toBeInTheDocument();
    expect(checks).toBe(0);
    unmount();
  }
});

it("shows the version in Settings, saves the launch-check toggle and checks on demand", async () => {
  const api = fakeBridge();
  const saved: boolean[] = [],
    checks: boolean[] = [];
  let reply: UpdateReply = { ...available, state: "current", latest: "0.2.0" };
  api.setCheckForUpdates = async (enabled) => {
    saved.push(enabled);
    return { problem: null };
  };
  api.checkForUpdate = async (manual) => {
    checks.push(manual);
    return reply;
  };
  let starts = 0;
  api.startUpgrade = async () => {
    starts++;
    return { problem: null };
  };
  const { host } = eventHost();
  render(<App api={api} host={host} />);
  await screen.findByText("Recall 0.2.0");
  fireEvent.click(screen.getByRole("button", { name: "Settings" }));
  expect(screen.getByText("Version: Recall 0.2.0")).toBeInTheDocument();
  const toggle = screen.getByRole("checkbox", {
    name: "Check for updates on launch",
  });
  expect(toggle).toBeChecked();
  fireEvent.click(toggle);
  await waitFor(() => expect(toggle).not.toBeChecked());
  expect(saved).toEqual([false]);
  fireEvent.click(screen.getByRole("button", { name: "Check now" }));
  expect(
    await screen.findByText("Recall 0.2.0 is up to date."),
  ).toBeInTheDocument();
  reply = available;
  fireEvent.click(screen.getByRole("button", { name: "Check now" }));
  expect(
    await screen.findByText("Recall 0.3.0 is available."),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Upgrade" }));
  await waitFor(() => expect(starts).toBe(1));
  expect(checks).toEqual([false, true, true]);
});

it("disables update controls in development builds", async () => {
  const api = fakeBridge();
  api.appInfo = async () => ({
    version: "dev",
    development: true,
    checkForUpdates: true,
  });
  const { host } = eventHost();
  render(<App api={api} host={host} />);
  await screen.findByText("Recall dev");
  fireEvent.click(screen.getByRole("button", { name: "Settings" }));
  expect(
    screen.getByRole("checkbox", { name: "Check for updates on launch" }),
  ).toBeDisabled();
  expect(screen.getByRole("button", { name: "Check now" })).toBeDisabled();
  expect(
    screen.getByText("Updates are only available in release installs."),
  ).toBeInTheDocument();
});

it("focuses Not now, not Upgrade, when the prompt opens", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  const { host } = eventHost();
  render(<App api={api} host={host} />);
  await screen.findByText("Recall 0.3.0 is available. You have 0.2.0.");
  const notNow = screen.getByRole("button", { name: "Not now" });
  await waitFor(() => expect(notNow).toHaveFocus());
  expect(screen.getByRole("button", { name: "Upgrade" })).not.toHaveFocus();
});
