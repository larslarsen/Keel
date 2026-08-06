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

The local program makes two kinds:

**Thumbnails.** Video images from YouTube's image server (`i.ytimg.com`), so it
can show you the videos it has recorded. Each is fetched once and stored
locally. No observation data is sent — these are ordinary image loads for videos
YouTube already showed you.

**The peer network.** Keel can join a peer-to-peer network to obtain
recommendation data other people have published, which is what lets it suggest
videos you have never been shown.

**At the default setting it asks the network for nothing at all.** It works from
a seed file that every user downloads identically, plus your own recording.
Asking a peer for a specific video would tell that peer which video you asked
about, so the default is to never ask.

Keel does not use the YouTube API, and never contacts any Keel-operated server,
because none exists. Peer discovery uses the public IPFS network, which nobody
in this project runs or controls.

### What other people on the network can see

This is the honest cost of a peer-to-peer design and we would rather state it
plainly than bury it.

**This does not apply at the default setting**, which asks for nothing.

Above the default, when Keel asks the network about a video, the peers it asks
can see **your IP address and which video you asked about**. This is the same
exposure any peer-to-peer system has — BitTorrent works the same way — and it is
the one place where using Keel is visible to strangers rather than only to
YouTube. The seed file limits it to unusual videos, because common ones are
already answered locally.

What they cannot see: your history, anything you have recorded, what you
actually watched, or any link between separate requests and a person. Keel's
network identity is a separate key from the one used to sign anything published,
so watching the network does not tell anyone what a node has contributed.

If this trade is not one you want, the peer network can be turned off entirely
and Keel keeps working on your own data alone.

## Contributing

Contributing is **off by default** and every level above the default is a
deliberate choice you make.

**Level 1 — personal (the default).** Nothing leaves your device, including
questions. Keel neither publishes nor asks; it works from the shared seed file
and your own recording. Downloading that seed is identical for every user, so it
says nothing about you.

**Level 2 — mirror.** Your computer stores and passes on data *other people*
published, using the disk space you allot, and may ask peers for videos the seed
does not cover. Nothing you observed is published, and the list of videos your
computer offers contains only what it is hosting for others — never what you
watched. The trade you are accepting is the request exposure described above.

**Levels 3 and 4** would publish aggregate or attributed records of what you
were recommended. **These are not built and nothing is sent at these settings
today.** Level 3 will be protected by threshold encryption — a report stays
sealed unless enough other people report the same thing, so anything only you
saw cannot be read — and nothing will be sent before that protection exists.

Anything published to a peer-to-peer network should be treated as permanent:
once another computer has a copy, it cannot be recalled.

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
