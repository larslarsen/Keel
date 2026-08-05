# **User Utility & SidePanel Architecture: The "Command Bridge"**

To solve the "Cold Start" problem, the extension must provide immediate, high-value utility that enhances the user's browsing experience without requiring them to "do" anything. The SidePanel is our "Command Bridge"—a browser-native sidebar that acts as an invisible, persistent diagnostic layer.

## **1\. The SidePanel "Command Bridge" (UI/UX)**

Instead of fighting YouTube's DOM (which is a never-ending source of tech debt), we use the Browser-Native SidePanel API.

* **Docked Persistence:** The SidePanel is anchored to the right side of the browser, perfectly aligned with the YouTube player. It does not "overlap"; it uses native browser layout space.  
* **DOM Immunity:** Because it is a native browser element, Google's UI updates cannot break our sidebar.  
* **State-Awareness:** The panel wakes up automatically when a video transition is detected, pulling metadata from the local Go daemon without the user needing to trigger a manual scan.  
* **"Bifurcated" Experience:** The user can toggle between \[Official\] (YouTube's native feed) and \[Custom\] (our audited metadata feed) with one click, giving the user control over their own attention.

## **2\. Seamless "Mindless Surfing" Features**

The utility must be implicit. The user should feel that their browsing has been "upgraded" the moment they install the tool.

### **A. The "Co-Pilot" Mode (Implicit Auditing)**

* **Auto-Attach:** When the extension detects a youtube.com URL, it automatically triggers chrome.sidePanel.setOptions to pin itself.  
* **Contextual Auditing:** As the user surfs, the SidePanel displays the "Alternative Flow" or "Audit Metrics" automatically, demystifying *why* the algorithm recommended the current video.

### **B. "True-Boolean" Search & Metadata Sorting**

YouTube’s search engine ignores operators to surface monetizable content.

* **Strict Mode:** A toggleable filter that forces actual compliance with "", \-, OR, and AND.  
* **Metadata Re-sorting:** Re-sort results by:  
  * **True Chronological:** Real upload timestamps, ignoring "relevance."  
  * **Subscriber count (Low-to-High):** To find "underground" content.  
  * **View count:** To separate original videos from stolen re-uploads.

### **C. Channel-Level "Hard" Block**

* **Persistent Command:** A "Block" button next to channel names in the sidebar.  
* **Client-Side Scrubbing:** The extension scrubs these channels from ytInitialData *before* the browser renders the elements. The user never sees the content, and it never enters the DOM.

### **D. Feed "Sanitization" Layers**

* **Category Presets:** Toggle "No Vlogs," "No Reactions," etc. The extension analyzes categoryId and keywords and hides matching videos automatically.  
* **Seed-Content Preservation:** If the user is tired of the algorithm, they can trigger a "Random Discovery" button to pull from the decentralized P2P index of "Unpopular/Low-View" videos, breaking the "Popularity Bias" loop.

## **3\. The "Dumb Extension / Smart Daemon" Split**

To ensure the SidePanel stays off the malware blacklist, we enforce strict separation of concerns.

* **The Extension is "Dumb":**  
  1. It performs passive DOM reading (like a translation tool).  
  2. It acts as a UI pipe to the local Go binary.  
  3. It contains *zero* proprietary algorithms or active scraping logic.  
  4. **Safety:** It uses standard Trusted Types policies for all DOM interactions, ensuring it remains "good citizen" compliant with Google's security model.  
* **The Daemon is "Smart":** The local Go binary handles the heavy lifting:  
  * Filtering/Blocking logic.  
  * IPFS storage and seeding.  
  * Graph-edge indexing.  
  * Adversarial resilience checks.

## **4\. Implementation Next Steps**

1. **Native Split-Pane:** Set sidePanel to default-open and pinned on youtube.com.  
2. **CSS-Injection:** Use content\_scripts to hide native elements when the "Custom Feed" toggle is active (non-invasive CSS toggles instead of JS DOM injection).  
3. **IPC Pipeline:** Define the Daemon \-\> Background Script \-\> SidePanel message bridge.