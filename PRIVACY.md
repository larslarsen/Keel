# Keel — Privacy Policy

Last updated: 5 August 2026

Keel records which videos YouTube recommends to you, so you can see and control
your own recommendations. This page describes exactly what it collects, where it
goes, and what it never does.

Every claim here is a property of how Keel is built, not a promise about how we
behave. The source is public and each one can be checked.

---

## The short version

**Nothing Keel observes leaves your device.** There is no account, no server
receiving your data, and no analytics. Keel cannot send your observations
anywhere, because no code exists that would.

---

## What Keel records

When you are on a YouTube watch page or the YouTube homepage, Keel records the
videos being **recommended to you**:

- Video ID, title, channel, duration, view count and published date
- Which position the recommendation appeared in, and on which surface
- Which video you were watching when it appeared
- When it was observed

That is the whole of it.

## What Keel does not record

- **Not what you watch.** Keel records what is *offered* to you, never what you
  play, how long you watch, or whether you finish.
- **Not your searches.** Search pages are out of scope entirely; Keel does not
  run there.
- **Not your identity.** No account, no email, no sign-in, no device
  fingerprint, no advertising ID.
- **Not your browsing outside YouTube.** The extension runs only on
  `www.youtube.com`.
- **Not page content.** Keel reads the rendered list of recommendations. It does
  not read comments, messages, or anything you type.

## Where it is stored

On your computer, in a SQLite database in your own user directory, written by a
program you install and run yourself.

The browser extension stores **no observation data at all** — not in
`localStorage`, not in IndexedDB, not in extension storage. The only thing the extension
keeps is one setting: whether recommendations are hidden. Your channel
blocklist, contribution level and disk-space limit all live in the local
database.

## Network activity

**The extension makes no network requests.**

The local program makes exactly one kind of request: fetching video thumbnail
images from YouTube's image server (`i.ytimg.com`), so it can show you the
videos it has recorded. Each image is fetched once and then stored locally. No
observation data is sent with these requests — they are ordinary image loads for
videos YouTube already showed you.

Keel does not use the YouTube API, and never contacts any Keel-operated server,
because none exists.

## Contributing — not available yet

Keel is designed to eventually let people **choose** to contribute aggregate
counts toward research into how recommendation systems behave. That is not
built, and **nothing is sent today at any setting.**

When it exists, it will be off by default and opt-in. Contributions will be
protected by threshold encryption: a report stays sealed unless enough other
people report the same thing, so anything only you saw cannot be read. Nothing
will be sent before that protection is in place.

If data handling ever changes, you will be told before it takes effect — not
through a quietly updated policy page.

## Your control

- **Export** — write every row and column Keel holds to a file, in JSON.
- **Wipe** — permanently delete every observation and shrink the database on
  disk. It cannot be undone.
- **Uninstall** — removing the local program stops all recording. Deleting its
  database directory removes everything it ever stored.

There is no step where you must ask us for your data, because we never have it.

## Third parties

None. No analytics, no advertising, no crash reporting, no hosted services.

## Children

Keel is not directed at children and collects nothing that identifies anyone.

## Contact

Questions, complaints, or security reports: open an issue at
<https://github.com/larslarsen/Keel/issues>.

## Changes to this policy

Material changes to what is collected or where it goes will be announced in the
extension itself before they take effect. Version history for this page is in
the repository.
