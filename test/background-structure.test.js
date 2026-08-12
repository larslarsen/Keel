// SPDX-License-Identifier: Apache-2.0
/**
 * WO-083's structural acceptance criteria, checked against the source rather
 * than against a reviewer's memory.
 *
 * Three properties are asserted here, and each one is a rule that decays
 * silently if nothing enforces it:
 *
 *   1. **No import cycle** in the extension's modules. A cycle means module
 *      initialisation order decides behaviour, which in a service worker is
 *      the kind of bug that only shows up after an eviction.
 *   2. **Storage has one owner in the control plane.** DESIGN_v2 §2.1 forbids
 *      observation data in browser storage. That is only auditable if there is
 *      exactly one file to audit — the moment a handler can reach
 *      `browser.storage` directly, the guarantee becomes "we checked every
 *      handler once".
 *   3. **`sw.js` is a composition root**, not a command switch. It was the
 *      latter, and two shipped defects came out of that shape.
 *
 * ## Scope of the storage rule, and why it is not the whole extension
 *
 * The rule is enforced over the **background control plane** (`background/*`
 * plus the transport in `lib/native.js`). Two surfaces outside it still read
 * storage directly, deliberately:
 *
 *   - `content/hide.js` reads the hide mode before first paint. Routing it
 *     through the service worker would put a message round-trip in front of a
 *     paint decision, which is the flicker WO-009 removed.
 *   - `sidepanel/index.js` reads the consent key for its nag banner and
 *     subscribes to `storage.onChanged`.
 *
 * Both read a *preference* the user set, never an observation, and both are
 * single-key reads with no writes of recorded data. Moving them would be a
 * behaviour change, which WO-083 forbids. Recorded as a boundary adjustment in
 * the work order rather than left implicit — see its "Boundary adjustments"
 * section. What the test below does enforce for them is the part that actually
 * matters: neither writes anything but the two known preference keys.
 */
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, resolve, relative } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const extRoot = resolve(here, "..", "extension");

/** Every .js file under extension/, repo-relative, excluding vendored dirs. */
function walk(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) {
      out.push(...walk(full));
    } else if (name.endsWith(".js")) {
      out.push(full);
    }
  }
  return out;
}

const files = walk(extRoot);
const rel = (f) => relative(extRoot, f).replace(/\\/g, "/");
const source = new Map(files.map((f) => [rel(f), readFileSync(f, "utf8")]));

/** Static `import ... from "..."` and `import("...")` specifiers, resolved. */
function importsOf(relPath) {
  const src = source.get(relPath) || "";
  const specs = [];
  const staticRe = /^\s*import\s[^;]*?from\s+["']([^"']+)["']/gm;
  const bareRe = /^\s*import\s+["']([^"']+)["']/gm;
  const dynRe = /\bimport\(\s*["']([^"']+)["']\s*\)/g;
  for (const re of [staticRe, bareRe, dynRe]) {
    let m;
    while ((m = re.exec(src))) specs.push(m[1]);
  }
  const from = dirname(relPath);
  return specs
    .filter((s) => s.startsWith("."))
    .map((s) => join(from, s).replace(/\\/g, "/"))
    .filter((p) => source.has(p));
}

/** The service worker's control plane: what WO-083 restructured. */
const CONTROL_PLANE = [
  "background/sw.js",
  "background/rpc.js",
  "background/panel_context.js",
  "background/prefs.js",
  "background/page_proofs.js",
  "lib/native.js",
];

describe("extension module structure (WO-083)", () => {
  it("has no import cycle anywhere in the extension", () => {
    const state = new Map(); // white | grey | black
    const cycles = [];

    function visit(node, stack) {
      state.set(node, "grey");
      for (const next of importsOf(node)) {
        if (state.get(next) === "grey") {
          cycles.push([...stack.slice(stack.indexOf(next)), next].join(" → "));
        } else if (!state.has(next)) {
          visit(next, [...stack, next]);
        }
      }
      state.set(node, "black");
    }

    for (const f of source.keys()) {
      if (!state.has(f)) visit(f, [f]);
    }
    assert.deepEqual(cycles, [], `import cycle(s): ${cycles.join("; ")}`);
  });

  it("gives the control plane exactly one storage owner", () => {
    const touchers = CONTROL_PLANE.filter((f) =>
      /\bstorage\b/.test(stripComments(source.get(f) || ""))
    );
    assert.deepEqual(
      touchers,
      ["background/sw.js", "background/prefs.js"],
      "only prefs.js may use storage; sw.js may only hand it to prefs.js"
    );

    // sw.js's single mention must be the injection itself and nothing more.
    const swStorage = stripComments(source.get("background/sw.js"))
      .split("\n")
      .filter((l) => /\bstorage\b/.test(l))
      .map((l) => l.trim());
    assert.deepEqual(swStorage, [
      "const prefs = createPrefs({ storage: browser.storage });",
    ]);
  });

  it("hands each control-plane module only the browser slice it needs", () => {
    const sw = stripComments(source.get("background/sw.js"));
    // The panel and the router are constructed without storage in their deps.
    for (const call of ["createPanelContext({", "createRpcRouter({"]) {
      const start = sw.indexOf(call);
      assert.ok(start > -1, `${call} not found in sw.js`);
      const args = sw.slice(start, sw.indexOf("});", start));
      assert.ok(
        !/storage/.test(args),
        `${call} must not receive browser storage`
      );
    }
  });

  it("keeps sw.js a composition root rather than a command switch", () => {
    const sw = stripComments(source.get("background/sw.js"));
    assert.ok(
      !/\bcase\s+["']/.test(sw),
      "sw.js must not dispatch RPCs; that belongs to background/rpc.js"
    );
    assert.ok(
      !/\bswitch\s*\(/.test(sw),
      "sw.js must not contain a command switch"
    );
    // The dispatcher is the module that does.
    assert.ok(/\bswitch\s*\(/.test(stripComments(source.get("background/rpc.js"))));
  });

  it("keeps the pure modules pure", () => {
    for (const f of ["background/page_proofs.js", "lib/prefs.js", "lib/protocol.js"]) {
      const src = stripComments(source.get(f) || "");
      assert.ok(
        !/\bbrowser\./.test(src) && !/\bchrome\./.test(src),
        `${f} must not touch a browser API — it is unit-testable by design`
      );
    }
  });

  it("writes only the two known preference keys, anywhere in the extension", () => {
    // The DESIGN_v2 §2.1 backstop: whatever else a surface reads, nothing in
    // the extension may put a recorded observation into storage.
    const writes = [];
    for (const [f, src] of source) {
      const clean = stripComments(src);
      const re = /storage(?:\??\.)local(?:\??\.)set\(\s*\{\s*\[?([A-Za-z_$][\w$]*)/g;
      let m;
      while ((m = re.exec(clean))) writes.push(`${f}: ${m[1]}`);
    }
    const allowed = /(HIDE_MODE_KEY|CONSENT_KEY)$/;
    const bad = writes.filter((w) => !allowed.test(w));
    assert.deepEqual(bad, [], `unexpected storage write(s): ${bad.join("; ")}`);
    assert.ok(writes.length > 0, "the scan found no writes at all — check the pattern");
  });
});

/** Strip block and line comments so prose about storage is not a violation. */
function stripComments(src) {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
}
