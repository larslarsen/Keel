// SPDX-License-Identifier: Apache-2.0
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { createLivePolicy } from "../extension/content/live_policy.js";

describe("content Live policy", () => {
  it("disarms on disconnect or downgrade and rescans on an idle Level-2 upgrade", () => {
    const events = [];
    const policy = createLivePolicy({ onEnable: () => events.push("arm-rescan"), onDisable: () => events.push("disarm") });
    policy.handle({ type: "CONTRIBUTION_STATUS", payload: { live: true, transition: "idle" } });
    assert.equal(policy.enabled(), true);
    policy.handle({ type: "DAEMON_STATUS", payload: { connected: false } });
    assert.equal(policy.enabled(), false);
    policy.handle({ type: "CONTRIBUTION_STATUS", payload: { live: true, transition: "idle" } });
    policy.handle({ type: "CONTRIBUTION_STATUS", payload: { live: false, transition: "idle" } });
    assert.deepEqual(events, ["arm-rescan", "disarm", "arm-rescan", "disarm"]);
  });
});
