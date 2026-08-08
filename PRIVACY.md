# Keel — Privacy Policy

Last updated: 5 August 2026

Keel records which videos YouTube recommends to you, so you can see and control
your own recommendations. This page describes exactly what it collects, where it
goes, and what it never does.

Every claim here is a property of how Keel is built, not a promise about how we
behave. The source is public and each one can be checked.

---

## The short version

**Your recording stays on your device.** There is no account, no server
receiving your data, and no analytics. The record of what you were shown, and of
which videos you opened, is never sent anywhere at any setting.

**One thing does leave, at every setting: when Keel sees a livestream it tells
other Keel users that the stream exists.** That notice is a video id and title
with no sender attached, so it says a stream is on, not who saw it. It is what
puts anything in the Live tab. Everything else on this page describes data that
stays put.

---

## What Keel records

When you are on a YouTube watch page or the YouTube homepage, Keel records the
videos being **recommended to you**:

- Video ID, title, channel, duration, view count and published date
- Which position the recommendation appeared in, and on which surface
- Which video it appeared beside — the id and title of the video you had open
- When it was observed

That is the whole of it.

### This means Keel holds a list of videos you opened

Worth stating outright rather than leaving to be inferred. Every recommendation
is recorded against the video it appeared beside, and that video is one you had
open — so the file on your computer contains, in effect, a list of the videos you
watched on pages where recommendations were captured, by id and by title.

It has to. A recommendation with no source is not a recommendation, and the
entire point of Keel is showing *what leads to what*.

What follows from a recommendation appearing in that list, and then later
appearing as a video you opened, is that you followed it. Keel can work that out
from what it already has. Nothing extra is collected to do so, and it is
computed on your machine like everything else.

## What Keel does not record

- **Not how you watch.** Keel records *which* videos you opened, because a
  recommendation has to come from somewhere — see below. It records nothing
  about your viewing of them: not whether you pressed play, how long you
  stayed, how far you got, or whether you finished. It cannot tell a video you
  watched to the end from one you closed after a second.
- **Not videos that recommended nothing.** A video is only noted if Keel
  captured recommendations beside it, and it runs only on watch pages and the
  homepage. Anything you reach from search, your subscriptions, a channel page
  or a link leaves no trace unless recommendations were recorded there.
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

**The peer network.** Keel joins a peer-to-peer network. What it does there
depends on your setting, and one part happens at every setting including the
default.

**At the default setting, Keel asks the network for nothing.** Asking a peer for
a particular video would tell that peer which video you asked about, so it does
not ask.

**At every setting, Keel announces livestreams it sees.** When a stream appears
in your recommendations, Keel tells other Keel users that the stream exists — a
video id and title. The notice carries **no sender**: not your name, not your
address, not even an identifier for your copy of Keel. Because of how the
network passes messages along, a computer receiving one cannot tell whether you
saw the stream or were simply relaying somebody else's notice. This is what
fills the Live tab, for you and for everyone.

Keel does not use the YouTube API, and never contacts any Keel-operated server,
because none exists. Peer discovery uses the public IPFS network, which nobody
in this project runs or controls.

### What other people on the network can see

This is the honest cost of a peer-to-peer design and we would rather state it
plainly than bury it.

**This does not apply at the default setting**, which asks for nothing.

Above the default, when Keel asks the network about a video, the peers it asks
can see **your IP address and which video you asked about** — though it asks in
batches of thousands, so which one of them you wanted is not visible. This is
the same kind of exposure any peer-to-peer system has, and it is the one place
where using Keel is visible to strangers rather than only to YouTube.

What they cannot see: your history, anything you have recorded, what you
actually watched, or any link between separate requests and a person. Keel's
network identity is a separate key from the one used to sign anything published,
so watching the network does not tell anyone what a node has contributed.

If this trade is not one you want, the peer network can be turned off entirely
and Keel keeps working on your own data alone.

## Contributing

Contributing is **off by default** and every level above the default is a
deliberate choice you make.

**Level 1 — personal (the default).** Nothing you record leaves your device, and
Keel asks the network for nothing. It works from what it has recorded here. The
one exception is the livestream notice described above, which carries nothing
about you and happens at every setting.

**Level 2 — mirror.** Your computer stores and passes on data *other people*
published, using the disk space you allot, and asks peers for recommendation
data in batches. Nothing you observed is published, and the list of data your
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
