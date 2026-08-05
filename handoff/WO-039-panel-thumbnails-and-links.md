# WO-039 — Thumbnails in the panel, and make panel titles links

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Open — current** |
| **Date** | 2026-08-04 |
| **Source** | Lars, 2026-08-04 |

---

## 1. Titles in the panel should be links

On the full page, search and suggestion titles are `<a>` elements pointing at
`https://www.youtube.com/watch?v=<id>`. In the panel they are plain text, so the one place you
actually read recommendations is the one place you cannot click them.

Make `#list .title` a link matching the full page: same URL shape, `target="_blank"`,
`rel="noreferrer"`. Keep the `slot_index` prefix outside the link — it is a position, not part of the
title.

Do not disturb the row's existing controls (the "why" button, Block/Unblock), and do not change
`renderPage`'s incremental `<li>` reuse, which exists so the list does not rebuild on every update.

## 2. Thumbnails — just fetch them

**Simplified 2026-08-04.** Earlier revisions of this ticket treated fetching thumbnails as a privacy
problem needing canvas capture and a local cache. That was disproportionate, and Lars was right to
push back.

What a fetch actually discloses: a request from the user's IP to `i.ytimg.com`, a static CDN that
sets no cookies, for a video ID **YouTube's own recommender just served to that account**. The same
user is browsing YouTube logged in from the same IP continuously. The marginal disclosure is
negligible, and it is exactly the request the page would have made had the rail not been hidden.

### Build it the simple way

- `<img src="https://i.ytimg.com/vi/<id>/mqdefault.jpg">` with `loading="lazy"`,
  `decoding="async"`, `referrerpolicy="no-referrer"`.
- Fixed box (96×54 in the panel, shrinking at the narrow breakpoint) with `object-fit: cover`, so a
  slow or missing image never reflows the list.
- Panel **and** full page. Consistency matters more than the marginal difference between them.

### The one case worth a note

Full-page search over history fetches many thumbnails at once for videos the user is not currently
watching. That is a slightly more distinctive pattern than the panel's, and `loading="lazy"` already
limits it to what is on screen. If it ever matters, a daemon-side cache fixes it — **not now**.

### Copyright still applies

Displaying an image is not redistributing it. `DESIGN_v2` §7.1 forbids storing and republishing
thumbnails, and that stands: **no thumbnail may enter a bundle, an export, or a published dataset.**
This ticket renders them and stores nothing.

### Update the publication claim

`PUBLISH_CHECKLIST.md` §4b says the extension "sends nothing to a remote server". That becomes false.
The accurate wording: Keel transmits **no observation data** off the device, and its only outbound
traffic is fetching thumbnail images from YouTube's CDN. Fix it in the same commit.

## 3. Not in this ticket

No canvas capture, no daemon-side image cache, no perceptual hash. Those belong to the preservation
plane (§7.1a) and stay deferred.

---

## Acceptance

- [ ] Panel titles are links to the watch URL; slot number stays outside the link.
- [ ] Existing row controls and incremental `<li>` reuse untouched.
- [ ] Thumbnails render in the panel and on the full page; a missing image leaves layout unchanged.
- [ ] No thumbnail is written to disk, a bundle, or an export.
- [ ] `PUBLISH_CHECKLIST.md` §4b updated: no observation data leaves the device, outbound traffic is
      thumbnail images only.
- [ ] 26 JS tests still pass.
