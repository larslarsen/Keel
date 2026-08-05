# **User Utility Architecture: The "Better Search & Feed" Engine**

The goal is to provide a "Better YouTube" experience that treats the user as an intelligent explorer. By moving the recommendation logic from a centralized ad-optimization machine to a decentralized, user-controlled graph, we can offer "True Engagement"—suggestions that align with the user's actual trajectory, not the platform's profit motive.

## **1\. The "True-Boolean" Search Engine**

YouTube's search engine is optimized for monetization, often ignoring specific operators to surface trending content.

* **Feature:** A toggleable "Strict Mode" that enforces client-side filtering on API results.  
* **Utility:**  
  * **Operator Compliance:** Fully obeys "", \-, OR, and AND.  
  * **Chronological Integrity:** Sort results by real upload timestamps, bypassing "relevance" re-rankings.  
  * **View-Count Sorting:** Easily separate original content from re-uploads.

## **2\. Channel-Level "Hard" Block**

YouTube's native "Don't Recommend" is a transient suggestion that frequently resets.

* **Feature:** A persistent, client-side blocklist.  
* **Utility:** Channels flagged by the user are scrubbed from the ytInitialData feed *before* the browser renders the page. They disappear as if they never existed.

## **3\. The Pro-Engagement Engine (The "Better Funnel")**

Instead of optimizing for "stickiness," the extension offers an interest-based recommendation system:

* **Semantic Graphing:** Suggestions are based on topical similarity and peer-to-peer "Successful User" paths. We don't recommend what's popular; we recommend what peers with the same interests found most valuable.  
* **The Entropy Slider:** A UI control allowing users to determine how "loose" the suggestions are.  
  * *0% (Focus):* Strict adherence to the current topic.  
  * *100% (Serendipity):* Broad discovery of high-quality niche content that peers have bookmarked.  
* **Community-Curation:** The "Funnel" is defined by the actual pathways power-users take, allowing for high-quality recommendations that aren't tainted by clickbait.

## **4\. Local-First Personalization (The LIV)**

To make recommendations actually useful, the daemon builds a **Local Interest Vector (LIV)** for the user.

* **Mechanism:** The daemon analyzes watched content locally to build a mathematical profile (e.g., {"coding": 0.8, "biology": 0.2}).  
* **Privacy Guardrail:** This profile *never* leaves the user's machine. It is used solely by the local daemon to rank the global index against the user's specific trajectory.  
* **Anonymity:** Recommendations are computed via local comparison with the decentralized global graph, ensuring zero "cloud-profile" tracking.

## **5\. Feed "Sanitization" Layers**

* **Tag/Category Presets:** Toggle off "Vlogs," "Reactions," or "Corporate News" based on categoryId and keywords.  
* **Seed-Content Preservation:** A "Random Discovery" button that pulls from the P2P index of low-view, high-relevance videos, effectively breaking the "Popularity Bias" loop.

## **6\. The "Algorithm-Viewer" (Transparency Layer)**

* **Funnel Inspector:** Clicking "Algorithm Info" on a video shows exactly why it was recommended. It exposes the hidden "reasons" (e.g., "Recommended because you watched Video X") to demystify the sidebar.