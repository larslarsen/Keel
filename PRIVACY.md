# Keel — Privacy Policy

Last updated: 12 August 2026

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

**One thing leaves at the default setting: requests.** Keel asks peers for the
graph/catalogue/search data its suggestions are built from, and for a global
word-popularity statistic, in batches of thousands so which one you wanted is
not visible. A request tells the peer answering it your address, the time, and
roughly which coarse slice of the catalogue you are interested in. That is a
real disclosure, and it is why this page does not describe the default as
network-silent — but it is a disclosure about *asking*, not about what you were
shown.

**Nothing derived from your recording leaves at the default setting.** Not a
recommendation, not a livestream you saw, not a word from your own library.
Publishing livestream sightings, contributing your word aggregate, and serving
broad recommendation claims all begin at Level 2 — “Broad sharing” — which you
choose deliberately. Raw rows and an ordered history are never sent at any
level.

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
database — as do, at Broad sharing, two running counts of requests answered
and bytes served, shown back to you as evidence your copy is doing useful
work. Both are plain totals with no peer id, query, prefix or per-request
timestamp behind them, and you can reset them at any time.

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
peers.** This is what makes suggestions and the shared graph work without a
server: your computer asks for a bucket of thousands of videos at once — never
one video by itself — and filters the answer locally, so a peer answering
cannot tell which one you actually wanted. Requesting a bucket still tells that
peer you asked, and roughly which coarse slice of the catalogue you're
interested in; see below for exactly what that discloses.

Searching *other people's* recommendations is the one network feature that
starts at Level 2 rather than at the default, and the reason is capacity rather
than privacy: those searches are answered by the machines that have chosen to
serve, so the level that answers them is the level that can ask. Searching what
Keel has recorded on your own device works at every setting.

**Announcing livestreams starts at Level 2.** When a stream appears in your
recommendations, a sharing node tells other Keel users the stream exists — a
video id and title. The notice carries **no sender field**: not your name and
not an application identifier for your copy of Keel, and once relayed its
payload does not say whether the sender saw the stream or forwarded somebody
else's notice.

That is deliberately not described as anonymous. A peer you are directly
connected to sees ordinary peer-to-peer connection metadata and can infer from
timing and topology that a notice started with you. Because a sighting is
derived from what you were shown, publishing one is a sharing decision, and it
is made at Level 2 rather than for you. **At the default setting Keel does not
join the Live topic, receive, relay, publish or serve the feed at all** — the
Live tab says so and points at the setting.

**Downloading the word statistic happens at every setting; contributing to it
starts at Level 2.** Every node fetches the same fixed-shape pack — how common
each word is across everyone's recordings — to draw the bars under the search
box. A sharing node also answers with its own, which includes the words in what
it was shown. That pack carries no plaintext word, video id, edge or search
query, but it is built so a guess at a specific word ("was 'giveaway' common?")
can still be checked against it. Aggregate disclosure is not zero disclosure,
which is why contributing one is a choice. The cost is accepted openly: the
global statistic under-counts everyone who has not opted in.

**None of the above makes the default node a host of anything.** Keel does not
answer another peer's request for graph, catalogue or search blocks — including
data it has itself fetched and cached — does not announce that it holds them,
does not serve the Live snapshot, and does not answer the word protocol. All of
that starts at Level 2, below, and so does sending recommendation claims derived
from the durable recording.

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

What a bucket request does not reveal is your history, what you actually
watched, or which member of the bucket you wanted. Separate requests can still
be linked within a connection or by IP address and timing. Keel's network
identity is separate from Level-2 claim keys, so a claim carries no durable
application identity, but that separation does not erase connection metadata.

If this trade is not one you want, the peer network can be turned off entirely
and Keel keeps working on your own data alone.

## Contributing

Contributing is **off by default** and every level above the default is a
deliberate choice you make.

**Level 1 — personal (the default).** Nothing you record or watch is published,
served to another peer, or told to anyone — that is what "personal" means here,
not "silent." Level 1 participates on the network described above, but only to
download: it fetches shared graph/catalogue/search data so suggestions and graph
pre-walk work, and fetches the global word-popularity statistic shown under the
search box. It searches what it has recorded on this device. What it never does
is announce a livestream it saw, contribute a word aggregate, answer another
peer's request for data, announce itself as holding anything, or publish what you
recorded. The shared Live feed and word contribution begin at Level 2.

One feature is held back rather than disclosed differently: searching other
people's recommendations. Nothing about it would leak more of yours — it is a
capacity boundary. Those searches are arbitrary, repeatable and answered
entirely by peers who have chosen to serve, and a network where everyone can
ask and a handful answer has no way to stay up. So the level that answers
searches is the level that can run them.

**Level 2 — broad sharing.** Everything at Level 1, plus searching other
people's recommendations, plus your computer starts **answering** other peers'
requests, using the disk space you allot. It answers
with two things at once, and the answer does not distinguish them: data other
people published that your computer is passing on, and **aggregated
recommendation blocks built from what you were shown**.

So at Level 2, something derived from your own recording does leave your
computer. Here is exactly what, and what keeps it from being a viewing history.

What leaves is a count: "on this video's page, this other video appeared, in
roughly this position, on roughly this day, this many times." No timestamps, no
page-visit ids, no titles, no searches, and no order — nothing that reconstructs
a session or says you watched anything, only that it was recommended alongside
something.

What carries it is the *batch*. Your computer never offers one video and never
answers a request for one video. It offers a batch of thousands, and answers
with everything it has in that batch at once — the same large batches it asks
in. Each locally produced neighbourhood has an opaque claim key unlinkable to
the keys for other neighbourhoods; updates preserve that key so an old claim is
replaced rather than multiplied. The key design does not prevent a peer from
linking deliveries by connection metadata or timing.

This is not zero disclosure and we will not describe it that way. Whoever
answers or receives a batch sees your IP address and sees the whole batch you
returned; timing and connection details can link one delivery to another. What
they do not get is a list of what you watched, in what order, or when.

**Levels 3 and 4** are not built and nothing is sent at these settings today.
Level 3 is not "the level where sharing starts" — Level 2 already contributes
the blocks described above. Level 3 adds a different mechanism: measurements
protected by threshold encryption, where a report stays sealed unless enough
other people report the same thing, so anything only you saw cannot be read.
Nothing will be sent at Level 3 before that protection exists. Level 4 would
publish records under a name deliberately traceable to you, which is the whole
point of it.

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
