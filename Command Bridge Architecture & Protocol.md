# **Command Bridge Protocol: Extension ↔ Go Daemon**

This document defines the strict, unidirectional message schema for the "Dumb Extension" and "Smart Daemon." This architecture minimizes the extension's logic, mitigating malware concerns by keeping all analytical "intelligence" within the local Go binary.

## **1\. Protocol Architecture**

The communication occurs via Chrome's Native Messaging API using runtime.sendNativeMessage (for episodic commands) and runtime.connectNative (for persistent streams).

### **A. The "Dumb" Extension (The Observer)**

* **Role:** Passive DOM reading and UI rendering.  
* **Constraints:** No proprietary algorithms; no active scraping. Uses Trusted Types for all DOM interaction.  
* **Responsibilities:**  
  * Injecting the SidePanel UI.  
  * Serializing ytInitialData to JSON.  
  * Forwarding User/UI Events (e.g., "User clicked block channel").

### **B. The "Smart" Daemon (The Brain)**

* **Role:** Local binary (Go) handling all filtering, indexing, and storage.  
* **Constraints:** High-performance, low-latency, system-level access.  
* **Responsibilities:**  
  * Filtering ytInitialData (Channel scrubbing).  
  * Building the Local Interest Vector (LIV).  
  * IPFS node management and metadata indexing.

## **2\. Message Schema (The "Command Bridge" Language)**

Every message follows a strict JSON structure: { "type": "COMMAND\_NAME", "payload": { ... } }.

### **Observation Layer (Extension → Daemon)**

* DOM\_REPORT: { "url": string, "yt\_state": object }  
  * *Trigger:* URL change or DOM mutation detected.  
* USER\_ACTION: { "action": "BLOCK\_CHANNEL" | "TOGGLE\_FEED" | "SYNC\_LIV", "data": object }  
  * *Trigger:* User interacts with the SidePanel UI.

### **Command Layer (Daemon → Extension)**

* FEED\_UPDATE: { "mode": "official" | "custom", "metadata": \[list of video objects\] }  
  * *Effect:* SidePanel re-renders the recommendation list.  
* METADATA\_INJECT: { "video\_id": string, "audit\_metrics": object }  
  * *Effect:* SidePanel displays the "Funnel Info" overlay.  
* SYSTEM\_STATUS: { "daemon\_running": boolean, "ipfs\_node\_ready": boolean }  
  * *Effect:* UI indicator for the user to see the "health" of their local tools.

## **3\. Security & Integrity Rules**

1. **Immutability:** The Daemon never modifies the YouTube DOM directly. It sends JSON instructions to the SidePanel, which renders them in its own isolated document.  
2. **Schema Enforcement:** If the Extension receives a message that does not conform to the schema (e.g., arbitrary code execution strings), it drops the packet and logs a security event to the console.  
3. **Local Isolation:** No LIV (Local Interest Vector) data is ever transmitted back to the browser; it exists only in the daemon's local memory/storage.

## **4\. Implementation Steps**

1. **Manifest Definition:** Configure nativeMessaging permission and define the side\_panel default path.  
2. **Daemon Scaffolding:** Implement the Go binary with a standard os.Stdin / os.Stdout listener for the 32-bit JSON protocol required by Chrome.  
3. **SidePanel Renderer:** Build the React/Preact or Vanilla JS loop that listens for FEED\_UPDATE messages and redraws the UI.