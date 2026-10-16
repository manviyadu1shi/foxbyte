# The agent row limit: why a row counter, and what it costs

The expert review plan (F2, gap G2) asked for two designs to be measured before
one was chosen for "policy by impact": refusing an agent's `UPDATE` or `DELETE`
by how many rows it changes, rather than by whether its text contains `WHERE`.

- **Row counter**, as built (`bb.guard_impact`): a `BEFORE UPDATE OR DELETE …
  FOR EACH ROW` trigger with a `WHEN` clause that only passes agent sessions to
  the function. The function counts rows per statement and table in a table only
  it can write, and refuses at the first row over the limit.
- **Transition table**: an `AFTER … FOR EACH STATEMENT` trigger with
  `REFERENCING OLD TABLE`. It counts the transition table once the statement has
  run, and refuses then.

## Results

PostgreSQL 18 (`ghcr.io/thefoxbyte/postgres-walg:18`) in the throwaway test VM
(Lima, aarch64, 4 CPUs, 4 GiB). Table `b(id int primary key, x int, pad text)`
with 1,000,000 rows of 40-character padding, rebuilt before every measurement.
Seven rounds, each running every case and design once, interleaved so drift in
the VM affects all of them alike. The figures are medians, in milliseconds.

| Case | No guard | Row counter | Transition table |
|---|---:|---:|---:|
| A person updates every row (`UPDATE b SET x = x + 1`) | 2,443 | 3,195 | 2,653 |
| A person deletes every row (`DELETE FROM b`) | 311 | 771 | 476 |
| An agent deletes every row (`DELETE FROM b WHERE 1=1`) | 273, not refused | **14.5, refused** | 505, refused |
| An agent updates 50 rows | 5.8 | 8.8 | 5.8 |

## Reading them

- **The row counter stops the damage, which is the point of a guardrail.** It
  refuses an agent's mass delete at row 101, in 14.5 ms, 35 times faster than
  the transition table. That design has to delete every row, keeping a copy of
  each, before it can count them and roll the whole statement back.
- **It costs people more on bulk writes.** About 0.46 µs per row deleted and
  0.75 µs per row updated, against 0.17 µs and 0.21 µs for the transition table.
  On ordinary statements of a few rows neither is noticeable. These are the
  cheapest writes PostgreSQL does (a narrow table, one index, no foreign keys),
  so on real tables the overhead is a smaller share.
- **Most of that cost is PostgreSQL's, not ours.** A per-row trigger whose
  `WHEN` is the constant `false` already adds about 0.3 µs per deleted row,
  because the executor has to fetch each old row for it.

## The WHEN clause, measured

The filter runs for every row of every `UPDATE` and `DELETE`, so its form
matters. Each variant below is the added cost of a person's 1,000,000-row
`DELETE`, over a trigger whose `WHEN` is `false`. These come from shorter runs
than the table above, and vary by about 100 ms between runs.

| `WHEN` | Added |
|---|---:|
| `current_setting('bb.actor_kind', true) = 'agent'` | ≈ 0 |
| `session_user ~~ 'agent%'` | ≈ 20 |
| `session_user = 'db_agent'` | ≈ 50 |
| `session_user ~~ ANY ('{agent%,db_agent}')` | ≈ 125 |
| `session_user = 'db_agent' OR left(session_user::text, 6) IN (…)` | ≈ 270 |
| `session_user ~ '^(agent[-:]\|db_agent$)'` | ≈ 300 |

Every mention of `session_user` looks the role's name up again, and a cast or a
regex costs more on top, so the clause names it once. The session setting alone
would be cheapest, but it cannot be the whole test: an agent can change it from
its own `WHERE` clause (`WHERE set_config('bb.actor_kind', 'human', true) …`) and
skip the guard. The login role is the part a session cannot change.

## Not done here

- **Limits across statements.** An agent can still change 100 rows at a time, a
  statement after another. Limits per agent per time window are the plan's next
  F2 step.
- **Approval.** An admin's override (`bb.allow_destructive=on`) is still the way
  to let one statement through. A one-time grant bound to the statement is part
  of F2.
- **An exemption with no cost.** A table that should have no limit still carries
  the trigger, and pays the per-row cost on bulk writes. Removing the trigger
  from such a table, rather than giving it no limit, is possible.

## Reproducing

The scripts are short shell loops over `psql` with `\timing` in a scratch
container with the Blackbox installed (`ledger.sql`, `ledger_ext.sql`,
`provenance.sql`, `datachanges.sql`). The transition-table trigger used:

```sql
CREATE FUNCTION bb.bench_tt() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE n bigint; BEGIN
  IF (SELECT actor_kind FROM bb._ctx()) IS DISTINCT FROM 'agent' THEN RETURN NULL; END IF;
  SELECT count(*) INTO n FROM oldrows;
  IF n > 100 THEN RAISE EXCEPTION 'tt: % rows', n; END IF; RETURN NULL; END $$;
CREATE TRIGGER bench_tt_d AFTER DELETE ON b REFERENCING OLD TABLE AS oldrows
  FOR EACH STATEMENT EXECUTE FUNCTION bb.bench_tt();
```
