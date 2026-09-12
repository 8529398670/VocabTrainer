# Vocab Trainer

A mobile-first vocabulary trainer. Swipe through cards drawn from a 46,000
word corpus graded by reading level, from 1st grade to doctoral. Spaced
repetition decides what comes back and when; three lists — known, not known,
skipped — are what you actually see.

Go + Fiber v3 on the server, plain browser JS on the front end, bolt for
storage, one self-contained binary when you ship it.

## Quick start

```bash
go build ./...
SECURE_COOKIES=false go run .
```

The first run prints a one-time login link. Open it — that is the entire
account system; there is no signup form and no passwords. Admins mint further
links from the Account screen or with `manage reissue-login`.

There are two kinds of link. A **login link** names one person and works once
— the original flow. An **invite link** is not tied to anyone and works a set
number of times: mint one for, say, 3 people, post it in the group chat, and
each of the first three to open it picks a name and gets an account. It stops
working after that, and an admin can withdraw it earlier from the Account
screen or with `manage revoke-invite`.

Opening an invite link costs nothing — a place is taken only when someone
submits the join form. That is deliberate: chat apps and mail scanners fetch
links to build previews, and a link that was spent by being *looked at* would
be empty before anyone tapped it.

`SECURE_COOKIES=false` matters on plain `http://localhost`: a `Secure` cookie
is silently dropped over http, so login appears to work and then does nothing.

## How it works

### The word list

46,050 words, each with WordNet definitions and examples, placed in one of 15
reading levels. The corpus is generated offline and compiled into the binary,
so there is nothing to install and no runtime dependency on any of the
sources.

Levels 1–12 are US school grades, anchored on age-of-acquisition ratings —
when people report having learned a word. Levels 13–15 are undergraduate,
graduate and doctoral, and are *ranked* rather than measured: no dataset says
what makes a word doctoral, so the hardest words are ordered and cut into
three bands instead of pretending to an absolute scale.

Three signals decide a word's level: age of acquisition, frequency across a
spoken corpus and a written one, and WordNet's semantic category. That last
one matters more than it sounds — without it the hardest levels fill up with
rare *concrete nouns* ("hogfish", "overshoe") rather than the abstract
vocabulary that actually marks advanced reading. Rarity is not difficulty.

See [ATTRIBUTION.md](ATTRIBUTION.md) for sources and licences, and the
comments on `estimateTier` in [cmd/builddata/main.go](cmd/builddata/main.go)
for the model itself.

To regenerate:

```bash
./scripts/fetch-corpus-sources.sh
go run ./cmd/builddata
```

### The cards

Each card shows one side and hides the other. Which side is a setting:

- **Show the word, hide the meaning** (default) — tests recall of meaning,
  which is what reading needs.
- **Show the meaning, hide the word** — harder, and the direction that helps
  with recall of the word itself.

Every card carries a small speaker beside the word. Pressing it reads the word
aloud through the device's own speech engine — the same one behind VoiceOver
and TalkBack. Nothing is downloaded and nothing is sent anywhere; the word
never leaves the device. The button is not drawn at all on a browser with no
speech engine, and in "show the meaning, hide the word" mode it appears only
once the word does, so it can never read out an answer you have not seen.

Tap reveals the hidden side. Then:

| Gesture | Key | Meaning |
|---|---|---|
| Swipe right | `→` | I already know it |
| Swipe left | `←` | I don't know it |
| Swipe up | `↑` | Skip — set it aside, no judgement |

Those are the defaults. Each of the three can be moved onto any of the four
directions in Settings — which way a thumb travels easily depends on the hand,
the phone, and the case it is in. The three are always different; whichever
direction is left over does nothing, and its arrow key stays the browser's.
The buttons show the direction each answer is on, and the arrow keys follow
the same mapping, so all three ways of answering agree.

Answering a card you have not turned over yet **reveals the hidden side before
the card leaves**, so a wrong guess is corrected on the spot. How long that
answer stays up is a setting: three seconds by default, anything from not at
all up to fifteen seconds, or until you answer again. A skip reveals nothing:
the point of a skip is not to engage with the word at all.

"I know it" can be excused from that reveal too, with a switch in Settings.
There is no wrong guess to correct when you already knew the word, so someone
clearing familiar ones is only being held up; with it on, "I know it" goes
straight to the next card. Off by default, and the other two answers are
unaffected.

### What a run is built from

By default a run is the reviews that have fallen due, topped up with words you
have never seen, up to the daily limit.

Settings can narrow that to **only the words you don't know**. That deck
ignores the scheduler entirely — every word you have marked as not known, most
overdue first, and nothing new — because someone who turns it on is asking to
work through their own failures now rather than wait for a card to come round.
It is the one deck with a bottom: clear it and there is nothing left to show.

### Scheduling

SM-2, adapted to the one bit of feedback a swipe gives. A card answered
"known" comes back in 4 days, then 10, then multiplied by an ease factor that
drifts up with success and down with lapses. A card answered "not known"
comes back in 10 minutes — inside the same session, which is most of why
relearning works — and climbs the early intervals again from the start.

Reviews that are due are never withheld; only *new* words are capped, by the
daily limit in Settings. Capping reviews is how a backlog becomes permanent.

### The lists

Every swipe files the word somewhere, and every list can move it elsewhere:

- **Not known** — answered "I don't know it". Review them any time; promote to
  known, skip, or forget entirely. This is also the list the practice-only
  deck above is built from.
- **Known** — answered "I already know it".
- **Skipped** — set aside, out of rotation. Unskipping returns a word to
  whichever list it came from, or to the new pool if it was skipped on sight.

That is also the recovery path for a mis-swipe, which on a gesture-driven
screen is not a rare event.

All three come out as a spreadsheet from Settings — one `.xlsx` file, one
sheet per list, with the definition, the level, and how the word has gone so
far. It is written by `server/xlsx`, which is about two hundred lines of the
standard library rather than a dependency: an .xlsx is a zip of XML, and the
subset a word list needs is small enough that taking on a spreadsheet library
would cost more in every binary than it saves here.

Progress, settings and history are per user, stored server-side.

## Layout

```
main.go                  wiring, plus the manage/version subcommands
cmd/builddata/           generates the word corpus from the raw sources
server/corpus/           the embedded word list: loading, levels, sampling
server/models/           user, session, login token, invite, progress, settings, stats
server/routes/           one file per group of endpoints; routes.go is the map
server/xlsx/             a minimal .xlsx writer, standard library only
static/js/train.js       the card stack and the swipe interaction
static/js/swipe.js       gesture recognition, no app logic
static/js/speech.js      the device's own text-to-speech, behind one button
language.yaml            every user-facing string in the app
```

Adding a screen means a file in `static/`, a file in `static/js/` with its own
`init()`, and its strings in `language.yaml`. Adding an endpoint means a file
in `server/routes/` registered in `routes.go`. Nothing needs a build step —
the browser runs the files exactly as they sit on disk.

## Shipping

```bash
./dockerRun.sh          # hardened Alpine container; use this for a deployment
./build.sh              # single-file binaries in dist/, no runtime deps
```

Both print the first-run login link on first start — it is shown once and
cannot be recovered. The binary is also the admin CLI: `./vocab-trainer
manage list-users`, `manage reissue-login -user-id 1`,
`manage create-invite -uses 3 -label "group chat"`, `manage paths`.

State lives in one directory (`~/.config/vocab-trainer/`, or `APP_DIR`): the
bolt database, a generated secret key, and an optional `config.yaml`. Back it
up as a unit — `app.db` is encrypted at rest and unreadable without
`secret.key`.
