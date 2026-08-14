// SPDX-License-Identifier: Apache-2.0
/**
 * Effective Live entitlement for a content script.
 *
 * The negotiated capability is deliberately not an input here: only an idle
 * Level-2+ contribution state enables outbound Live observation. A daemon
 * disconnect is fail-closed and invokes the same teardown as a downgrade.
 */
export function createLivePolicy({ onEnable = () => {}, onDisable = () => {} } = {}) {
  let enabled = false;
  function wanted(msg) {
    return msg?.type === "CONTRIBUTION_STATUS" &&
      msg.payload?.live === true && msg.payload?.transition === "idle";
  }
  return {
    handle(msg) {
      if (msg?.type !== "CONTRIBUTION_STATUS" && msg?.type !== "DAEMON_STATUS") return false;
      const next = wanted(msg);
      if (next === enabled) return false;
      enabled = next;
      if (next) onEnable(); else onDisable();
      return true;
    },
    set(value) { enabled = value === true; },
    enabled: () => enabled,
  };
}
