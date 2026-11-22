import { View, div } from "gpui-kit";
import { checking, closeOverlay, exec, focused, hc, home, openOverlay, props, run, setShared, shared, spaces, terminals } from "herdr";

const KEY = "popup-scratch";
const SIZE = 0.81;
const KEEP = true;
const LIFE = ["--keep"];

// one idle shell per space, hidden and shown again until it exits
async function target(space) {
  return { slot: space?.id ?? "", argv: [] };
}

// overlay ids don't survive an app restart; shared state does
const SESSION = String(Math.random());

const live = (addr) => terminals().some((t) => t.address === addr && t.state === "live");

async function start(space, cwd, argv) {
  const on = space?.machine ? ["--on", space.machine] : [];
  const dir = cwd ? ["--cwd", cwd] : [];
  const r = await hc(["run", "--bg", "--json", "--no-parent", ...on, ...dir, ...LIFE, "--label", `ws.popup=${KEY}`, "--", ...argv]);
  if (r.code !== 0) throw new Error(r.stderr || r.stdout);
  return JSON.parse(r.stdout).address;
}

async function toggle(launcher) {
  const f = focused();
  const space = spaces().find((s) => s.id === f?.space);
  const cwd = f?.cwd || space?.folder || (space?.machine ? null : home());
  const state = shared(KEY) ?? {};
  const shown = Object.entries(state).find(([, v]) => v?.overlay && v.session === SESSION && live(v.terminal));

  if (shown) {
    const [slot, cur] = shown;
    closeOverlay(launcher);
    closeOverlay(cur.overlay);
    if (KEEP) {
      setShared(KEY, { ...state, [slot]: { terminal: cur.terminal } });
    } else {
      run("terminal.kill", { terminal: cur.terminal });
      setShared(KEY, { ...state, [slot]: undefined });
    }
    return;
  }

  const { slot, argv } = await target(space, cwd);
  const cur = state[slot];
  const terminal = KEEP && cur?.terminal && live(cur.terminal) ? cur.terminal : await start(space, cwd, argv);
  // the launcher must close first or the keyboard returns to the pane below
  closeOverlay(launcher);
  const overlay = openOverlay({
    kind: "dialog",
    modal: true,
    width: SIZE,
    height: SIZE,
    layout: { id: KEY, type: "block", content: { terminal } },
  });
  setShared(KEY, { ...state, [slot]: { terminal, overlay, session: SESSION } });
}

export default class Popup extends View {
  init(_props, cx) {
    if (checking()) return;
    // holding the keyboard when this closes hands it to the popup
    this.handle = cx.focus_handle();
    this.handle.focus();
    cx.spawn(async (cx) => {
      await cx.sleep(0);
      const launcher = props().overlay;
      try {
        await toggle(launcher);
      } catch (e) {
        closeOverlay(launcher);
        console.log(`${KEY}: ${e}`);
      }
    });
  }

  render() {
    return this.handle ? div().track_focus(this.handle) : div();
  }
}
