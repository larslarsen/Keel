// background.js - The "Dumb Pipe"
// This script acts purely as a message relay between the SidePanel and the local Go daemon.
// It contains no proprietary logic and is designed to fail gracefully if the daemon is missing.

const DAEMON_HOST_NAME = 'com.auditbridge.youtube';

/**
 * RELAY: Forwards messages from the SidePanel to the Go Daemon.
 * Uses chrome.runtime.connectNative for persistent streaming connections.
 */
chrome.runtime.onConnect.addListener((port) => {
    if (port.name !== "audit-bridge-pipe") return;

    let nativePort = null;

    try {
        nativePort = chrome.runtime.connectNative(DAEMON_HOST_NAME);

        // Forward messages from SidePanel -> Daemon
        port.onMessage.addListener((message) => {
            if (nativePort) {
                nativePort.postMessage(message);
            }
        });

        // Forward messages from Daemon -> SidePanel
        nativePort.onMessage.addListener((message) => {
            port.postMessage(message);
        });

        nativePort.onDisconnect.addListener(() => {
            nativePort = null;
            port.postMessage({ type: "SYSTEM_STATUS", payload: { daemon_running: false } });
        });

    } catch (err) {
        console.warn("Audit Bridge Daemon not detected. Ensure native host is installed.");
        port.postMessage({ 
            type: "SYSTEM_STATUS", 
            payload: { daemon_running: false, error: "Daemon not found" } 
        });
    }

    port.onDisconnect.addListener(() => {
        if (nativePort) nativePort.disconnect();
    });
});

/**
 * AUTO-OPEN: Pin the SidePanel when the user hits YouTube.
 */
chrome.sidePanel
  .setPanelBehavior({ openPanelOnActionClick: true })
  .catch((error) => console.error(error));

chrome.tabs.onUpdated.addListener(async (tabId, info, tab) => {
  if (!tab.url) return;
  const url = new URL(tab.url);
  if (url.hostname === 'www.youtube.com' && url.pathname.includes('/watch')) {
    await chrome.sidePanel.setOptions({
      tabId,
      path: 'sidepanel.html',
      enabled: true
    });
  }
});