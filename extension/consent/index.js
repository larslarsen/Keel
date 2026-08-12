// SPDX-License-Identifier: Apache-2.0
/**
 * First-run consent (WO-049, corrected by WO-089).
 *
 * DESIGN_v2 §"Disclosure" requires an in-extension consent screen, not just a
 * store listing. Nothing is observed until a choice is stored — the observer
 * checks the same value before it arms.
 *
 * WO-089 added the second half: the screen also covers what Keel *downloads*,
 * and the affirmative answer has to reach the desktop app, not only this
 * browser profile. The daemon is a separate long-lived process that starts with
 * no browser attached and is what actually opens connections, so a permission
 * stored here alone could never have governed it. The service worker performs
 * both writes in order — daemon first, then this profile's observation flag —
 * so there is no window in which recording is on against a disclosure the
 * desktop app has not acknowledged.
 *
 * Declining must leave a working extension that records nothing and networks
 * nothing. If declining broke the product it would not be a choice, and the
 * screen would be theatre.
 */
import { browser } from "../lib/browser.js";
import { errText } from "../lib/errors.js";

const el = {
  accept: document.getElementById("btn-accept"),
  decline: document.getElementById("btn-decline"),
  choices: document.getElementById("choices"),
  status: document.getElementById("status"),
};

function setBusy(busy) {
  if (el.accept) el.accept.disabled = busy;
  if (el.decline) el.decline.disabled = busy;
}

async function choose(value) {
  setBusy(true);
  el.status.textContent = "";
  try {
    const r = await browser.runtime.sendMessage({
      type: "SET_CONSENT",
      payload: { consent: value },
    });
    // The service worker reports a refusal as {ok:false, error}. Treating that
    // as success is the failure this screen must not have: it would tell the
    // user recording was on while the desktop app sat at its gate.
    if (r && r.ok === false) throw new Error(r.error || "could not save that");
    el.choices.hidden = true;
    el.status.textContent =
      value === "granted"
        ? "Recording is on. Open a YouTube video and Keel will start."
        : "Nothing will be recorded and nothing will be downloaded. Everything else still works.";
  } catch (err) {
    setBusy(false);
    const msg = errText(err);
    // The most likely failure by far is that the desktop app is not running or
    // is out of date, and neither is fixed by pressing the button again — so
    // say which one it is rather than reporting a generic save error.
    el.status.textContent = /not connected|update required|consent unavailable/i.test(msg)
      ? `Keel's desktop app has to be running and up to date before it can ` +
        `accept this. (${msg})`
      : `Could not save that: ${msg}`;
  }
}

el.accept?.addEventListener("click", () => choose("granted"));
el.decline?.addEventListener("click", () => choose("declined"));
