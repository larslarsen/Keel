// SPDX-License-Identifier: Apache-2.0
/** Classic content-script entry: load ESM observer (no bundler). */
(async () => {
  const api = globalThis.browser ?? globalThis.chrome;
  if (!api?.runtime?.getURL) return;
  try {
    await import(api.runtime.getURL("content/observer.js"));
  } catch (err) {
    // Formatted inline, not via lib/errors.js: bootstrap is a classic content
    // script, not a module, so it cannot import anything. This is also the one
    // line that reports a failure to load the module graph at all — the moment
    // when nothing else is available to report it. Printing the raw object here
    // says "[object Object]", which is how a broken extension looks like a
    // broken button instead of a missing file.
    const detail =
      (err && (err.message || err.name)) ||
      (typeof err === "string" ? err : "") ||
      "unknown error";
    console.error(
      "[Keel] observer load failed:",
      String(detail),
      "-",
      api.runtime.getURL("content/observer.js"),
    );
  }
})();
