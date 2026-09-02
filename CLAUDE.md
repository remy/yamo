# CLAUDE.md

## Read this first

**Read [knowledge.md](knowledge.md) before changing anything.** It is the
document for whoever is changing the code: what the program is, how it is
split, why the awkward decisions were made that way, and the traps that have
already been fallen into. The README is the user-facing document; `knowledge.md`
is the one that will stop you undoing a deliberate choice.

In particular, check it before you:

- add a field to the catalogue snapshot (§7 — incremental scanning means a new
  field needs a version bump or it stays empty forever);
- touch search, fuzzy matching or the query language (§5);
- change how tags are read or written (§5, §7);
- reach for a filesystem watcher, an auto-spawned server, or a generated
  handler — all three were considered and rejected, with reasons (§3, §5).

## Keep it written

`knowledge.md` is part of the work, not a chore afterwards. When a change alters
the code's structure or the reasoning behind it, update the document in the same
change:

- **Structure.** A new package, a moved responsibility, or a boundary that shifts
  belongs in the package map (§4), with the line counts refreshed.
- **Reasoning.** If a decision would look strange to the next person without the
  reasoning — an optimisation that reads as redundancy, a rejected alternative,
  a constraint imposed by something outside this repo — write it into §5 or the
  relevant section. Record what was tried and did not work, not just what shipped.
- **Traps and bugs.** A bug that was invisible to the obvious test goes in §10;
  a footgun in the tooling or the environment goes in §12.
- **Numbers.** Performance figures (§8), format support (§7), operation counts
  (§6) and test counts (§2) are claims about the code — correct them when they
  stop being true.

Write for someone who has never seen the code and has no access to this
conversation. Prose, not bullet fragments; explain *why*, since *what* is already
in the source.

## Conventions

- Go, one static binary, no runtime dependencies. Keep the direct dependency
  list as short as it is.
- `make test` runs everything under `-race`; it must stay clean.
- The server is the only process that opens the catalogue or a music file.
  Everything else is a client of the HTTP API.
- `api/openapi.yaml` is the contract. Routes and schema are checked against each
  other in both directions by `internal/api/conformance_test.go`.
