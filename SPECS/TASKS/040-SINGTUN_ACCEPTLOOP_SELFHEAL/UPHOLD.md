# UPHOLD — 040-SINGTUN_ACCEPTLOOP_SELFHEAL

<!-- The pass is run by a fresh judge: clean context, read-only.
     Exactly three inputs: FEATURE.md, the diff, Touches from the task's SPEC.md.
     The implementer's narrative is never passed to the judge.
     A ledger is chronological by nature — that is why it lives here, in the
     task folder; FEATURE.md keeps only the current status of promises. -->

| Field | Value |
|---|---|
| Judge | <model/agent, date> |
| Diff | <commit range> |
| Touches | P2, P5 |
| Promises judged | <N, highest PN> |

<!-- "Promises judged" fixes the SET this pass ruled on. A ledger is a snapshot
     of one hearing, not a standing verdict: promises added to the feature
     afterwards were never before this judge, and the ledger must not appear to
     cover them. check-uphold.sh compares this figure with the feature as it
     stands now and reports the gap — new promises need a new hearing, and that
     hearing is the feature's own court (/audit), not a reopening of this task.
     Write it as the count plus the highest ID ("8, P8"): the pair catches the
     case where one promise is retired and another added, which a count alone
     would miss. -->

## Betrayal candidates

<!-- Before any verdicts: at least 3 candidates across the whole feature — the
     most convincing ones the judge can build, each with a concrete failure
     scenario. An empty list is not a green light, it is a halt: "you did not
     look hard enough".
     Every candidate is run to ground and ends in a fate line — machine-checked:
       fate: BETRAYED → PN        landed as a betrayal in row PN
       fate: CANNOT VERIFY → PN   deferred in row PN (with its needed:)
       fate: REFUTED — <evidence> killed, with the quote/command that killed it
     An abandoned candidate is an unfinished pass; a REFUTED without evidence
     is a verdict without evidence — void. -->

1. <scenario: which promise, how and under what conditions it could be betrayed>
   fate: <BETRAYED → PN / CANNOT VERIFY → PN / REFUTED — evidence>
2. <…>
3. <…>

## Ledger

<!-- One row for EVERY promise of the feature, no exceptions.
     A row is filled completely before moving to the next one; batch-approving
     several rows in one message is a violation.
     Order inside a row is strict: reasoning first, then structure. -->

### P1. <promise wording>

**Reasoning:** <run the promise's witnesses ("the one where …") against the diff — what in the diff touches the promise, how it could have been betrayed; for untouched promises — indirect impact: shared helpers, configuration, initialization order>

```
locus:    <file:line — the place that currently fulfils the promise>
killer:   <the minimal change that would betray the promise; why it is not in the diff>
evidence: <verbatim quote of code/diff, or a command with its output>
grade:    static / synthetic / live / device / field
link:     <one sentence: why the evidence proves the verdict>
verdict:  HOLDS | UNTOUCHED | BETRAYED | CANNOT VERIFY
needed:   <only after CANNOT VERIFY, and then mandatory: the concrete missing
          check — run/device/log/testbed. This line IS the queue entry:
          corpus-map.sh collects every needed: into the verification queue,
          so a deferral named only in prose is a deferral lost>
```

### P2. <…>

## Completion call

<!-- The numbers are checked mechanically against the promise count in FEATURE.md. -->

Promises total: N. Holds with evidence: n1. Betrayed: n2. Untouched (with evidence): n3. Deferred: n4.

- [ ] promise statuses in FEATURE.md updated after the pass
- [ ] every BETRAYED verdict resolved: <fixed in this task / new task / author's decision to change the promise>
