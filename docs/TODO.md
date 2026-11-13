# Creator to-do system

Creator's to-do board (`/todo` or `/tasks`) is a session-scoped, verification-gated checklist for real project work. It is deliberately not generated for trivial one-shot requests; the agent decides whether a mission has enough scope to justify it, and the user can force it by explicitly asking for a to-do list.

## Who owns what

- **The board/system** owns IDs, ordering, dependencies, focus rules, status, age, receipts, and the presentation.
- **The AI** fills in the imperative steps it believes are required, the `doing` phrasing, the acceptance detail, optional dependencies, and an executable `verify` command for anything checkable.
- **The user** asks for the work. The agent does not invent unrelated work.

## How it renders

`/todo` opens a transparent, compact checklist:

```text
to do
type to filter · ↑/↓ select · enter inspect · esc back

██████████░░░░  5/8 done · 3 verified · 0 blocked · 1 flagged
› [>] t3  run the test suite · verify · running the suite
  [x] t2  add the API route      verify
  [x] t4  add regression coverage verify
```

Symbols:

- `[>]` — in progress
- `[ ]` — pending
- `[x]` — done
- `[B]` — blocked on an unfinished dependency
- `[-]` — cancelled

The detail view shows the acceptance criteria without exposing internal mechanics. Expand/select/go back with `↑`/`↓`, `PgUp`/`PgDn`, `enter`, `esc`.

## Verification gates

A task carrying `verify: <command>` cannot close until that command was actually run and exited 0; the resulting job id is attached as a machine receipt. Non-oracle tasks close only with written evidence. Three or more closures without any oracle adds a pre-summary nudge. Dependencies make a dependent task unstartable until upstream work closes.

## How the AI uses it

Tasks are added imperatively when the mission is nontrivial and then started one at a time. The board is always one session at a time: resumed sessions see only their own checklist.
