// SPDX-License-Identifier: Apache-2.0
/**
 * First-run consent (WO-049).
 *
 * DESIGN_v2 §"Disclosure" requires an in-extension consent screen, not just a
 * store listing. Nothing is observed until a choice is stored — the observer
 * checks the same value before it arms.
 *
 * Declining must leave a working extension that records nothing. If declining
 * broke the product it would not be a choice, and the screen would be theatre.
 */
import { browser } from "../lib/browser.js";

const el = {
  accept: document.getElementById("btn-accept"),
  decline: document.getElementById("btn-decline"),
  choices: document.getElementById("choices"),
  status: document.getElementById("status"),
};

async function choose(value) {
  try {
    await browser.runtime.sendMessage({
      type: "SET_CONSENT",
      payload: { consent: value },
    });
    el.choices.hidden = true;
    el.status.textContent =
      value === "granted"
        ? "Recording is on. Open a YouTube video and Keel will start."
        : "Nothing will be recorded. Everything else still works.";
  } catch (err) {
    el.status.textContent = `Could not save that: ${err?.message || err}`;
  }
}

el.accept.addEventListener("click", () => choose("granted"));
el.decline.addEventListener("click", () => choose("declined"));
