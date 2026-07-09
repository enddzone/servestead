# AGENTS.md

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:

- State your assumptions explicitly.
- If requirements are ambiguous and the choice affects the outcome, ask.
- If uncertainty is technical and can be resolved by inspection or a small experiment, do that instead of guessing.
- If an action is cheap and reversible, state the assumption and proceed.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.

## 2. Simplicity First

**Minimum code that solves the problem without creating a dead end. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- Implement the complete requested behavior, not a partial approximation.
- Don't remove or weaken existing behavior to make a change pass unless explicitly requested.
- Match implementation and verification rigor to the change's risk.
- If you write 200 lines and it could be 50, rewrite it.

Prefer the simplest reversible solution. If the simple path creates hidden coupling, irreversible structure, migration work, security risk, or likely rework, call that out before implementing.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify. Also ask: "Does this shortcut make the next likely change harder?" If yes, choose a sounder foundation.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:

- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

Scope follows the requested behavior, not arbitrary file boundaries. Make necessary cross-file changes, but do not use them as an excuse for unrelated cleanup.

When your changes create orphans:

- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:

- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

Always run `golangci-lint` after making changes, in addition to any targeted tests or broader test suite runs that fit the risk of the change.

For multi-step tasks, state a brief plan:

```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

At completion, report:

- What changed.
- How it was verified.
- What was not changed or verified.
- Assumptions made.
- Any remaining risks, follow-up work, or relevant issues discovered but left untouched.

## 5. Constructive Challenge

**Act as a reasoning partner, not just an executor.**

- If there is a clearly better approach, say so before implementing.
- Explain the alternative and its tradeoffs concisely.
- Prefer established project patterns and industry-standard solutions over reinvention.
- Challenge decisions when they create security risk, data loss, irreversible work, broad refactors, or substantial wasted effort.
- Do not turn minor style preferences or small tasks into strategy discussions.
- If the requested approach is reasonable, note the better option and proceed.
- Stop and ask before proceeding only when the requested approach is unsafe, likely wrong, or materially wasteful.

---

**These guidelines are working if:** diffs stay focused, solutions remain easy to change, important assumptions and alternatives surface early, and skipped work is reported explicitly.

## OpenWiki

This repository has documentation located in the /openwiki directory.

Start here:
- [OpenWiki quickstart](openwiki/quickstart.md)

OpenWiki includes repository overview, architecture notes, workflows, domain concepts, operations, integrations, testing guidance, and source maps.

When working in this repository, read the OpenWiki quickstart first, then follow its links to the relevant architecture, workflow, domain, operation, and testing notes.
