# Keel — Privacy Policy

Last updated: 11 August 2026

Keel records recommendation surfaces on YouTube and the scrolling feed presented
to you on TikTok, so you can see and control what recommender systems show you.
This page describes exactly what it collects, where it goes, and what it never
does.

Every claim here is a property of how Keel is built, not a promise about how we
behave. The source is public and each one can be checked.

---

## The short version

**Your recording stays on your device.** There is no account, no server
receiving your data, and no analytics. The record of what you were shown, which
videos you opened, and which TikTok clips the feed presented is never sent as a
raw history, published, or served to another peer at any setting.

**Three things do leave, at every setting, including the default: requests for
shared data, livestream notices, and a word-popularity aggregate.** Keel asks
peers for the graph/catalogue/search data suggestions are built from, in
batches of thousands so which one you wanted is not visible. When it sees a
livestream it tells other Keel users the stream exists — a video id and title
with no sender field; a directly connected peer may still infer an origin from
network topology and timing after it is relayed. And it exchanges a whole,
fixed-shape word-popularity pack with peers — no plaintext words, ids, edges or
query, but not a zero-disclosure aggregate either. These three are detailed
below. Everything else on this page describes data that stays put.

---

## What Keel records

On a YouTube watch page or the YouTube homepage, Keel records the
videos being **recommended to you**:

- Video ID, title, channel, duration, view count and published date
- Which position the recommendation appeared in, and on which surface
- Which video it appeared beside — the id and title of the video you had open
- When it was observed

On supported TikTok feed/video surfaces there is no visible “up next” rail to
record. Keel instead records the clips the rendered feed presents, in scroll
order: video id, caption, creator, hashtags and sound id when present, plus when
the clip was observed. Hashtag and sound clusters stay local. Fields for dwell
and engagement exist for a future local mirror, but the current observers do not
reliably collect them; this policy must change before collection does.

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

- **Not how you watch.** On YouTube, Keel records *which* videos you opened, because a
  recommendation has to come from somewhere — see below. It records nothing
  about your viewing of them: not whether you pressed play, how long you
  stayed, how far you got, or whether you finished. It cannot tell a video you
  watched to the end from one you closed after a second.
- **Not YouTube videos that recommended nothing.** A YouTube video is only noted if Keel
  captured recommendations beside it, and it runs only on watch pages and the
  homepage. Anything you reach from search, your subscriptions, a channel page
  or a link leaves no trace unless recommendations were recorded there.
- **Not raw searches.** Current search pages are out of scope. If a later
  search→feed feature is enabled, only a local cryptographic hash may be stored;
  the raw query may never be persisted.
- **Not your identity.** No account, no email, no sign-in, no device
  fingerprint, no advertising ID.
- **Not your browsing outside the named platforms.** The extension has access
  only to `www.youtube.com` and `www.tiktok.com`, and stays idle off supported
  surfaces on those sites.
- **Not unrelated page content.** Keel reads only the rendered recommendation/
  feed fields listed above. It does not read comments, messages, or anything
  you type.

## Where it is stored

On your computer, in a SQLite database in your own user directory, written by a
program you install and run yourself.

The browser extension stores **no observation data at all** — not in
`localStorage`, not in IndexedDB, not in extension storage. The only thing the extension
keeps is UI preferences and recording consent. Your channel
blocklist, contribution level and disk-space limit all live in the local
database.

## Network activity

**The extension makes no network requests.**

The local program makes two kinds:

**Thumbnails.** Video images from YouTube's image server (`i.ytimg.com`), so it
can show you the videos it has recorded. Each is fetched once and stored
locally. No observation data is sent — these are ordinary image loads for videos
YouTube already showed you.

**The peer network.** Keel joins a peer-to-peer network at every setting,
including the default. It is not silent there — it just does not serve or
publish anything of yours.

**At every setting, Keel fetches shared graph, catalogue and search data from
peers.** This is what makes suggestions and peer search work without a server:
your computer asks for a bucket of thousands of videos at once — never one
video by itself — and filters the answer locally, so a peer answering cannot
tell which one you actually wanted. Requesting a bucket still tells that peer
you asked, and roughly which coarse slice of the catalogue you're interested
in; see below for exactly what that discloses.

**At every setting, Keel announces livestreams it sees.** When a stream appears
in your recommendations, Keel tells other Keel users that the stream exists — a
video id and title. The notice carries **no sender field**: not your name and not
an application identifier for your copy of Keel. Once relayed, its payload does
not say whether you saw the stream or forwarded somebody else's notice. A
directly connected peer still sees ordinary peer-to-peer connection metadata
and may infer an origin from topology and timing. This is what fills the Live
tab, for you and for everyone.

**At every setting, Keel exchanges a whole word-popularity aggregate.** Every
node fetches and answers the same fixed-shape statistics pack — how common each
word is across everyone's recordings, including your own local corpus. It
carries no plaintext word, video id, edge or search query, but it is built so
that a guess at a specific word ("was 'giveaway' common?") can still be checked
against it. That is an aggregate disclosure, not zero disclosure, and it is why
this page does not describe the default setting as network-silent.

**None of the above serves, stores or passes on anything for other people.**
Keel does not answer another peer's request for graph, catalogue or search data
— including data it has itself fetched and cached — and does not tell the
network it holds anything, at the default setting. That starts at Level 2,
below.

Keel does not use the YouTube API, and never contacts any Keel-operated server,
because none exists. Peer discovery uses the public IPFS network, which nobody
in this project runs or controls.

### What other people on the network can see

This is the honest cost of a peer-to-peer design and we would rather state it
plainly than bury it. It applies at every setting, including the default —
Level 1 asks the network for shared data as described above, it just never
answers a request itself.

When Keel asks the network for shared data, the peers it asks can see **your IP
address and which batch of thousands of videos you asked about** — though it
asks in those large batches specifically so which one of them you wanted is not
visible. This is the same kind of exposure any peer-to-peer system has, and it
is the one place, beyond the livestream notice and word aggregate above, where
using Keel is visible to strangers rather than only to YouTube.

What they cannot see: your history, anything you have recorded, what you
actually watched, or any link between separate requests and a person. Keel's
network identity is a separate key from the one used to sign anything published,
so watching the network does not tell anyone what a node has contributed.

If this trade is not one you want, the peer network can be turned off entirely
and Keel keeps working on your own data alone.

## Contributing

Contributing is **off by default** and every level above the default is a
deliberate choice you make.

**Level 1 — personal (the default).** Nothing you record or watch is published,
served to another peer, or told to anyone — that is what "personal" means here,
not "silent." Level 1 is a full participant on the network described above: it
fetches shared graph/catalogue/search data so suggestions and peer search work,
announces livestreams it sees, and exchanges the word-popularity aggregate.
What it never does is answer another peer's request for data, announce itself
as holding anything, or publish what you recorded.

**Level 2 — mirror.** Everything at Level 1, plus your computer starts
**answering** other peers' requests — storing and passing on data *other
people* published, using the disk space you allot. Nothing you observed is
published, and the list of data your computer offers contains only what it is
hosting for others — never what you watched. The trade you are accepting is the
same request exposure described above, now from the answering side too.

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
