# WO-043 — Hiding must follow the panel, not be permanent

| | |
|---|---|
| **Addressee** | Anyone |
| **Status** | **Done** |
| **Date** | 2026-08-04 |

## Wanted

The eye toggle turns hiding **on**. While it is on:

- Panel **open** → YouTube's recommendation rail is hidden.
- Panel **closed** → the rail comes back; YouTube looks untouched.

The rail is suppressed to make room for the panel. With no panel there is nothing
to make room for.

## Current behaviour

The eye works, but hiding is permanent — the rail stays hidden with the panel
closed.

## How it got here

- **WO-009** built this correctly, with three modes: `never` / `with-panel` /
  `always`, defaulting to `with-panel`. Lars QA'd it and it worked.
- **WO-017** collapsed the three modes to on/off at Lars's request. Both
  `with-panel` and `always` were mapped to `on`, which silently discarded the
  panel gate. Simplifying the *control* was approved; changing what it *does*
  was not.
- Two later attempts to restore the gate (`88a9d65`, `feda2ad`) were reverted
  after appearing not to work. They may in fact have been correct and tested
  against a stale content script — see **Testing**.

## Confirmed intact — do not re-investigate

Checked directly against the current tree:

- **The service worker is untouched by WO-017.** `runtime.onConnect` counts
  `sidePanelPorts`, and `broadcastHideState()` fires on both connect and
  disconnect, carrying `panelOpen`.
- **`broadcastHideState()` reaches content scripts.** It calls both
  `broadcast()` (extension pages) and `broadcastToYoutubeTabs()`
  (`tabs.sendMessage`). WO-009 established that `runtime.sendMessage` alone does
  **not** reach content scripts; that fix is still in place.
- **The panel opens its port** — `runtime.connect({ name: SIDEPANEL_PORT })` is
  present in `sidepanel/index.js`.
- **`startHide()` runs unconditionally** at module load in `observer.js`
  `start()`, before `listenSpa()` and `onNavigate()`. It is not surface-gated.

The plumbing that carries panel state to the content script is all present. Only
the two consumers below were changed.

## The change

Two files.

**1. `extension/lib/prefs.js`** — `shouldHide` currently ignores its second
argument:

```js
export function shouldHide(mode, _panelOpen) {
  return coerceHideMode(mode) === "on";
}
```

It must respect it:

```js
export function shouldHide(mode, panelOpen) {
  return coerceHideMode(mode) === "on" && Boolean(panelOpen);
}
```

Legacy `always` coerces to `on` and is therefore gated too. That is intentional:
there is no three-state control any more, and a stored value behaving
differently from anything the UI can produce is a trap for whoever debugs it
next.

**2. `extension/content/hide.js`** — `panelOpen` must be tracked. WO-017 removed
the assignments when the value stopped being used, so restore all three points:

- Declare `let panelOpen = false;` beside `let mode`.
- In `setHideState(state)`:
  `if (state && typeof state.panelOpen === "boolean") panelOpen = state.panelOpen;`
- In `startHide()`, from the `GET_HIDE_STATE` reply:
  `if (typeof r.panelOpen === "boolean") panelOpen = r.panelOpen;`
- `effective()` passes it: `return shouldHide(mode, panelOpen);`

**A partial fix is worse than none here.** Adding the gate without the
assignments makes `panelOpen` permanently `false` and hiding never happens —
that is exactly what `88a9d65` did.

`test/prefs.test.js` asserts the old semantics and must be updated: `on` with
`panelOpen` true hides, `on` with false does not, `off` never does.

## Testing — where the previous attempts went wrong

**A content-script change needs a hard page reload, not just an extension
reload.** `hide.js` is an ES module already evaluated in the page; reloading the
extension does not replace it. The same trap appeared in the WO-009 round.

1. Reload the extension at `brave://extensions`.
2. **Hard-reload the YouTube tab** (Ctrl+Shift+R).
3. Open the panel, eye on → rail hidden.
4. Close the panel → rail returns, with no page reload.
5. Reopen → hidden again.

At each step, in the page console:

```js
document.getElementById('keel-hide-recommendations')
```

Element present = hiding active; `null` = not. **Measure this rather than judging
by eye.** It is the only unambiguous signal, and every failed attempt in this
ticket came from inferring instead of checking.

## Acceptance

- [ ] Panel open + eye on → style element present, rail hidden.
- [ ] Panel closed → style element gone, rail visible, no reload needed.
- [ ] Eye off → rail visible regardless of panel state.
- [ ] Verified against a hard-reloaded tab.
- [ ] `test/prefs.test.js` updated; 26 JS tests pass.
