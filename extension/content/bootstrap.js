// SPDX-License-Identifier: Apache-2.0
/** Classic content-script entry: load ESM observer (no bundler). */
(async () => {
  const api = globalThis.browser ?? globalThis.chrome;
  if (!api?.runtime?.getURL) return;
  try {
    await import(api.runtime.getURL("content/observer.js"));
  } catch (err) {
    console.error("[Keel] observer load failed", err);
  }
})();
