// SPDX-License-Identifier: Apache-2.0
/**
 * One way to turn anything thrown, rejected or received into readable text.
 *
 * Every diagnostic surface this extension has stringifies what it is given:
 * console lines, the extension's own error list on chrome://extensions (which
 * INSTALL.md tells people to paste when reporting a problem), and any status
 * text rendered into the panel. Hand any of them an object and the user sees
 * "[object Object]" — the least useful thing a diagnostic can say.
 *
 * The idiom this replaces, `err?.message || err`, looks safe and is not: it
 * falls through to the raw value for anything without a .message, and the two
 * cases that matter most both lack one. A protocol envelope is
 * {v, id, type, payload}. An Error does not survive structured cloning across
 * runtime.sendMessage — it arrives as {}. Both stringify to "[object Object]",
 * and both are exactly what a failing daemon connection produces.
 *
 * Live QA lost a session to this: the daemon had sent a perfectly clear reason
 * and none of it reached the screen.
 */

/**
 * @param {unknown} err anything: Error, ERROR payload, envelope, string, null
 * @returns {string} never "[object Object]", never empty
 */
export function errText(err) {
  if (err === null || err === undefined) return "(no detail)";
  if (typeof err === "string") return err || "(no detail)";
  if (typeof err !== "object") return String(err);

  const rec = /** @type {Record<string, unknown>} */ (err);

  // Error, and the {message, code} ERROR payload the daemon sends.
  const message = typeof rec.message === "string" ? rec.message : "";
  const code = typeof rec.code === "string" ? rec.code : "";
  if (code && message) return `${code}: ${message}`;
  if (message) return message;

  // A whole envelope: the useful part is inside the payload.
  if (rec.payload && typeof rec.payload === "object") {
    const inner = errText(rec.payload);
    if (inner !== "(no detail)") {
      return typeof rec.type === "string" ? `${rec.type} — ${inner}` : inner;
    }
  }
  if (code) return code;

  // A cloned Error arrives as {}. Say so, rather than printing "{}".
  try {
    const json = JSON.stringify(err);
    if (!json || json === "{}") return "(error lost in transit)";
    return json.length > 300 ? `${json.slice(0, 300)}…` : json;
  } catch {
    return Object.prototype.toString.call(err);
  }
}
