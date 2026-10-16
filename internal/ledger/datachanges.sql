-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Blackbox: data changes (audit v2 G04). Installed after ledger.sql and the 2.0
-- files, on every start; idempotent.
--
-- The Blackbox's event triggers see DDL only. TRUNCATE fires no event trigger,
-- so `TRUNCATE orders` was neither refused like DROP TABLE nor recorded, and a
-- DELETE or UPDATE — the ordinary way an agent destroys data — left no trace.
-- Postgres runs table triggers for both, so every user table gets these:
--
--   bb_guard_truncate  BEFORE TRUNCATE, per statement: the guardrail and the
--                      record for TRUNCATE, for everyone (bb.guard_truncate)
--   bb_record_dml      AFTER UPDATE OR DELETE, per statement: records the
--                      statement when an agent ran it (bb.record_dml)
--   bb_guard_dml       BEFORE UPDATE OR DELETE, per statement: refuses an
--                      agent's statement with no WHERE (bb.guard_dml)
--   bb_guard_impact    BEFORE UPDATE OR DELETE, per row, agents only: refuses an
--                      agent's statement once it changes more rows of the table
--                      than its limit (bb.guard_impact; ordinary tables only)
--
-- Humans' UPDATE and DELETE are not recorded: that would put a Blackbox write
-- in every data-changing statement on main. An agent's are, because proving
-- what an agent did is the point. Row counts are not recorded — counting needs
-- transition tables, which would copy every changed row of every statement.
--
-- The triggers are attached to every existing table here, and to each new one
-- by an event trigger (bb_data_attach). Dropping, disabling or renaming one is
-- refused unless the guardrail override is in force (a superuser or a db_admin
-- member with bb.allow_destructive=on). A superuser can still switch triggers
-- off for a session; the Blackbox has always had that limit.

-- TRUNCATE joins DROP TABLE and DROP SCHEMA as blocked by default. An install
-- whose policy already lists it keeps its choice.
INSERT INTO bb.policy(op, action) VALUES ('TRUNCATE', 'block') ON CONFLICT (op) DO NOTHING;
-- An agent's DELETE or UPDATE with no WHERE at all: the ordinary way a table is
-- emptied by accident. Recording it was not enough — "an agent cannot silently
-- wipe your rows" is only true if something refuses. Blocked for agents by
-- default, and for agents only: a person doing it meant to, and a migration
-- legitimately rewrites every row.
INSERT INTO bb.policy(op, action) VALUES ('UNQUALIFIED DML', 'block') ON CONFLICT (op) DO NOTHING;

-- Is the guardrail on? Imports and pipelines switch it off for their own load
-- by disabling bb_guard_start; TRUNCATE follows the same switch.
CREATE OR REPLACE FUNCTION bb._guard_on() RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT coalesce((SELECT evtenabled <> 'D' FROM pg_event_trigger WHERE evtname = 'bb_guard_start'), true);
$$;

-- The identity event triggers give a table, for trigger functions: schema and
-- name, each quoted only when it has to be.
CREATE OR REPLACE FUNCTION bb._table_identity(schema_name name, table_name name) RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT quote_ident(schema_name) || '.' || quote_ident(table_name) $$;

CREATE OR REPLACE FUNCTION bb.guard_truncate() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, bb AS $$
DECLARE act text; allow text; c record; st text := 'APPLIED'; obj text;
BEGIN
  obj := bb._table_identity(TG_TABLE_SCHEMA, TG_TABLE_NAME);
  SELECT * INTO c FROM bb._ctx();
  SELECT action INTO act FROM bb.policy WHERE op = 'TRUNCATE';
  IF act = 'block' AND bb._guard_on() THEN
    allow := coalesce(nullif(current_setting('bb.allow_destructive', true), ''), 'off');
    IF NOT (allow IN ('on','true','1') AND bb._may_override()) THEN
      -- Recorded through a second connection so the record survives the
      -- rollback the refusal causes (as bb.guard_ddl_start does).
      IF bb._holds_chain_lock() THEN
        RAISE WARNING 'guardrail: this blocked attempt is not recorded in Blackbox, because this transaction has already written to it';
      ELSE
        PERFORM dblink_exec(
          'host=/var/run/postgresql dbname=' || current_database() || ' user=' || current_user,
          format($f$INSERT INTO bb.schema_ledger
                  (actor,actor_kind,tool,session,branch,command_tag,object_type,object_identity,statement,status,risk)
                  VALUES (%L,%L,%L,%L,%L,'TRUNCATE','table',%L,%L,'BLOCKED','policy')$f$,
            c.actor, c.actor_kind, c.tool, c.session, c.branch, obj, current_query()));
      END IF;
      RAISE EXCEPTION 'guardrail: TRUNCATE is blocked by policy (set bb.allow_destructive=on to override)'
        USING ERRCODE = 'insufficient_privilege',
              HINT = 'Only superusers and members of db_admin may override. Grant it with: fox admin grant <email>';
    END IF;
  END IF;
  IF act = 'flag' THEN
    st := 'FLAGGED';
  END IF;
  INSERT INTO bb.schema_ledger
    (actor,actor_kind,tool,session,branch,command_tag,object_type,object_identity,statement,status,risk)
  VALUES (c.actor,c.actor_kind,c.tool,c.session,c.branch,'TRUNCATE','table',obj,current_query(),st,'truncate');
  RETURN NULL;
END;
$$;

-- Does this statement carry a WHERE at all? Deliberately generous: any WHERE
-- anywhere in the statement text counts, including one inside a subquery, so the
-- guard only ever refuses a statement that plainly has no condition. It errs
-- towards letting work through, because the cost of a false refusal is a person
-- unable to do their job, and the cost of a false allow is an entry in the record
-- that says what happened.
CREATE OR REPLACE FUNCTION bb._has_condition(q text) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT coalesce(q, '') ~* '\mwhere\M' $$;

CREATE OR REPLACE FUNCTION bb.guard_dml() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, bb AS $$
DECLARE act text; allow text; c record; obj text; q text;
BEGIN
  SELECT * INTO c FROM bb._ctx();
  -- Agents only. A human's unqualified DELETE is their own decision, and this
  -- guard is about a tool acting on its own.
  IF c.actor_kind IS DISTINCT FROM 'agent' THEN
    RETURN NULL;
  END IF;
  q := current_query();
  IF bb._has_condition(q) THEN
    RETURN NULL;
  END IF;
  SELECT action INTO act FROM bb.policy WHERE op = 'UNQUALIFIED DML';
  IF act IS DISTINCT FROM 'block' OR NOT bb._guard_on() THEN
    RETURN NULL;   -- warn and allow are handled by the record, as everywhere else
  END IF;
  allow := coalesce(nullif(current_setting('bb.allow_destructive', true), ''), 'off');
  IF allow IN ('on','true','1') AND bb._may_override() THEN
    RETURN NULL;
  END IF;
  obj := bb._table_identity(TG_TABLE_SCHEMA, TG_TABLE_NAME);
  -- Recorded through a second connection, so the record survives the rollback the
  -- refusal causes (as bb.guard_truncate does).
  IF bb._holds_chain_lock() THEN
    RAISE WARNING 'guardrail: this blocked attempt is not recorded in Blackbox, because this transaction has already written to it';
  ELSE
    PERFORM dblink_exec(
      'host=/var/run/postgresql dbname=' || current_database() || ' user=' || current_user,
      format($f$INSERT INTO bb.schema_ledger
              (actor,actor_kind,tool,session,branch,command_tag,object_type,object_identity,statement,status,risk)
              VALUES (%L,%L,%L,%L,%L,%L,'table',%L,%L,'BLOCKED','unqualified')$f$,
        c.actor, c.actor_kind, c.tool, c.session, c.branch, TG_OP, obj, q));
  END IF;
  RAISE EXCEPTION 'guardrail: % with no WHERE is blocked for agents by policy (set bb.allow_destructive=on to override)', TG_OP
    USING ERRCODE = 'insufficient_privilege',
          HINT = 'Add a WHERE. To let one through, an admin sets bb.allow_destructive=on for the session (fox admin grant <email> gives that right).';
  RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION bb.record_dml() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, bb AS $$
DECLARE c record;
BEGIN
  SELECT * INTO c FROM bb._ctx();
  IF c.actor_kind IS DISTINCT FROM 'agent' THEN
    RETURN NULL;
  END IF;
  INSERT INTO bb.schema_ledger
    (actor,actor_kind,tool,session,branch,command_tag,object_type,object_identity,statement,status,risk)
  VALUES (c.actor,c.actor_kind,c.tool,c.session,c.branch,TG_OP,'table',
          bb._table_identity(TG_TABLE_SCHEMA, TG_TABLE_NAME),current_query(),'APPLIED',NULL);
  RETURN NULL;
END;
$$;

-- ── Policy by impact (expert review plan, G2) ────────────────────────────────
-- "No WHERE" is read from the statement's text, so `DELETE FROM t WHERE 1=1`
-- empties a table as surely as `DELETE FROM t` and passes. What matters is how
-- many rows a statement changes, so an agent's UPDATE or DELETE is also refused
-- once it has changed more rows of one table than that table's limit allows.
--
-- Counted row by row (bb_guard_impact, BEFORE UPDATE OR DELETE FOR EACH ROW),
-- and refused at the first row over the limit: a mass delete stops after a
-- hundred rows rather than running to the end and being rolled back. The other
-- design, an AFTER statement trigger reading a transition table, holds a copy of
-- every changed row of every statement, people's included, and can only refuse
-- once all the work is done (docs/impact-guard-benchmark.md measures both).
--
-- The trigger's WHEN clause keeps people's writes out of it: a row is passed to
-- the function only when the login role or the injected kind says agent, and
-- the function then asks bb._ctx, which decides. The count is kept in a table
-- only the guard can write, never in a session setting: an agent's own WHERE
-- clause could reset a setting row by row.
--
-- A statement is one message from the client (statement_timestamp), so several
-- statements sent in one message are counted together. That errs towards
-- refusing. A partition counts towards its partitioned table.
INSERT INTO bb.policy(op, action) VALUES ('AGENT DML ROWS', 'block') ON CONFLICT (op) DO NOTHING;

-- The limits themselves (bb.dml_limits, 100 rows by default) and their
-- history are in ledger.sql, beside bb.policy: created there before the event
-- triggers are on, so creating them is not recorded as a change.

-- The count so far, per backend and table, for the statement it was started
-- by. Unlogged: it means nothing after a restart, and is never replicated.
CREATE UNLOGGED TABLE IF NOT EXISTS bb.dml_counter (
  pid      int NOT NULL,
  rel      oid NOT NULL,
  stmt     timestamptz NOT NULL,
  n        bigint NOT NULL,
  max_rows bigint,             -- NULL: no limit applies to this statement
  action   text,
  PRIMARY KEY (pid, rel)
);

-- The limit for a table: its own row, else the default, else none.
CREATE OR REPLACE FUNCTION bb._dml_limit(root oid) RETURNS bigint
LANGUAGE sql STABLE AS $$
  SELECT l.max_rows FROM bb.dml_limits l
  WHERE l.rel = '*' OR l.rel = (SELECT bb._table_identity(n.nspname, c.relname)
                                FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                                WHERE c.oid = root)
  ORDER BY (l.rel = '*') LIMIT 1;
$$;

CREATE OR REPLACE FUNCTION bb.guard_impact() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, bb AS $$
DECLARE
  root oid := coalesce(pg_partition_root(TG_RELID), TG_RELID);
  ts   timestamptz := statement_timestamp();
  me   int := pg_backend_pid();
  k    record; c record; have_c boolean := false;
  cur bigint; lim bigint; act text; allow text; obj text;
BEGIN
  SELECT n, max_rows, action INTO k FROM bb.dml_counter WHERE pid = me AND rel = root AND stmt = ts;
  IF FOUND THEN
    IF k.max_rows IS NULL THEN
      IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
      RETURN NEW;
    END IF;
    UPDATE bb.dml_counter SET n = n + 1 WHERE pid = me AND rel = root RETURNING n INTO cur;
    lim := k.max_rows; act := k.action;
  ELSE
    -- This statement's first row in this table: decide once whether a limit
    -- applies, and to whom.
    SELECT * INTO c FROM bb._ctx();
    have_c := true;
    IF c.actor_kind = 'agent' AND bb._guard_on() THEN
      SELECT action INTO act FROM bb.policy WHERE op = 'AGENT DML ROWS';
      allow := coalesce(nullif(current_setting('bb.allow_destructive', true), ''), 'off');
      IF act IN ('block','flag') AND NOT (allow IN ('on','true','1') AND bb._may_override()) THEN
        lim := bb._dml_limit(root);
      END IF;
    END IF;
    DELETE FROM bb.dml_counter WHERE pid = me AND stmt <> ts;   -- this backend's earlier statements
    INSERT INTO bb.dml_counter (pid, rel, stmt, n, max_rows, action) VALUES (me, root, ts, 1, lim, act)
      ON CONFLICT (pid, rel) DO UPDATE SET stmt = EXCLUDED.stmt, n = 1,
        max_rows = EXCLUDED.max_rows, action = EXCLUDED.action;
    cur := 1;
  END IF;
  IF lim IS NOT NULL AND cur = lim + 1 THEN
    obj := (SELECT bb._table_identity(n.nspname, cl.relname) FROM pg_class cl
            JOIN pg_namespace n ON n.oid = cl.relnamespace WHERE cl.oid = root);
    IF NOT have_c THEN
      SELECT * INTO c FROM bb._ctx();
    END IF;
    IF act = 'flag' THEN
      INSERT INTO bb.schema_ledger
        (actor,actor_kind,tool,session,branch,command_tag,object_type,object_identity,statement,status,risk)
      VALUES (c.actor,c.actor_kind,c.tool,c.session,c.branch,TG_OP,'table',obj,current_query(),'FLAGGED','impact');
    ELSE
      -- Recorded through a second connection, so the record survives the
      -- rollback the refusal causes (as bb.guard_dml does).
      IF bb._holds_chain_lock() THEN
        RAISE WARNING 'guardrail: this blocked attempt is not recorded in Blackbox, because this transaction has already written to it';
      ELSE
        PERFORM dblink_exec(
          'host=/var/run/postgresql dbname=' || current_database() || ' user=' || current_user,
          format($f$INSERT INTO bb.schema_ledger
                  (actor,actor_kind,tool,session,branch,command_tag,object_type,object_identity,statement,status,risk)
                  VALUES (%L,%L,%L,%L,%L,%L,'table',%L,%L,'BLOCKED','impact')$f$,
            c.actor, c.actor_kind, c.tool, c.session, c.branch, TG_OP, obj, current_query()));
      END IF;
      RAISE EXCEPTION 'guardrail: % of more than % row(s) of % is blocked by policy for agents (set bb.allow_destructive=on to override)', TG_OP, lim, obj
        USING ERRCODE = 'insufficient_privilege',
              HINT = format('Narrow the statement. An admin can let one through with bb.allow_destructive=on, or change this table''s limit: fox guardrail limit --table %s <rows>', obj);
    END IF;
  END IF;
  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;

-- The WHEN clause that keeps people's writes out of bb.guard_impact: a login
-- role the Blackbox reads as an agent (agent-<id>, agent:<email>, db_agent), or
-- a shared role whose injected kind says agent. It is evaluated for every row
-- of every UPDATE and DELETE, so it names session_user once, in one LIKE ANY:
-- each mention is a catalogue lookup per row, and a cast or a regex costs more
-- (docs/impact-guard-benchmark.md). It is a coarse filter: anyone whose role
-- name begins with "agent", or whose session claims to be one, reaches the
-- function, which asks bb._ctx and lets a person by.
CREATE OR REPLACE FUNCTION bb._impact_when() RETURNS text
LANGUAGE sql IMMUTABLE AS $$
  SELECT $w$(session_user ~~ ANY ('{agent%,db_agent}') OR current_setting('bb.actor_kind', true) = 'agent')$w$
$$;

-- Attach the triggers to one table, where they are missing. Found by function,
-- not name, so a trigger renamed out of the way still counts as present (and
-- bb.data_protect_end refuses the rename anyway).
--
-- The impact guard goes on ordinary tables only, partitions included: a row
-- trigger on a partitioned table would be cloned onto partitions that may
-- already have their own, and bb.guard_impact counts a partition's rows
-- towards its root anyway.
CREATE OR REPLACE FUNCTION bb._attach_data_triggers(rel regclass) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, bb AS $$
BEGIN
  IF (SELECT relkind FROM pg_class WHERE oid = rel) = 'r'
     AND NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = rel AND tgfoid = 'bb.guard_impact()'::regprocedure) THEN
    EXECUTE format('CREATE TRIGGER bb_guard_impact BEFORE UPDATE OR DELETE ON %s FOR EACH ROW WHEN %s EXECUTE FUNCTION bb.guard_impact()',
                   rel, bb._impact_when());
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = rel AND tgfoid = 'bb.guard_truncate()'::regprocedure) THEN
    EXECUTE format('CREATE TRIGGER bb_guard_truncate BEFORE TRUNCATE ON %s FOR EACH STATEMENT EXECUTE FUNCTION bb.guard_truncate()', rel);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = rel AND tgfoid = 'bb.record_dml()'::regprocedure) THEN
    EXECUTE format('CREATE TRIGGER bb_record_dml AFTER UPDATE OR DELETE ON %s FOR EACH STATEMENT EXECUTE FUNCTION bb.record_dml()', rel);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = rel AND tgfoid = 'bb.guard_dml()'::regprocedure) THEN
    EXECUTE format('CREATE TRIGGER bb_guard_dml BEFORE UPDATE OR DELETE ON %s FOR EACH STATEMENT EXECUTE FUNCTION bb.guard_dml()', rel);
  END IF;
END;
$$;

-- The tables the triggers belong on: ordinary and partitioned tables outside
-- the system schemas and the Blackbox's own.
CREATE OR REPLACE FUNCTION bb._data_tables() RETURNS SETOF regclass
LANGUAGE sql STABLE AS $$
  SELECT c.oid::regclass FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE c.relkind IN ('r','p') AND c.relpersistence <> 't'
    AND n.nspname NOT IN ('bb','pg_catalog','information_schema','pg_toast')
    AND n.nspname NOT LIKE 'pg_temp%' AND n.nspname NOT LIKE 'pg_toast_temp%';
$$;

-- Is the override in force for this session? (Dropping a Blackbox trigger is as
-- destructive as anything the guardrail blocks.)
CREATE OR REPLACE FUNCTION bb._override_in_force() RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT coalesce(nullif(current_setting('bb.allow_destructive', true), ''), 'off') IN ('on','true','1')
     AND bb._may_override();
$$;

-- ddl_command_end: attach to new tables; refuse a disabled, replica-only or
-- renamed trigger. Replica-only (ALTER TABLE … ENABLE REPLICA TRIGGER) is
-- disabling by another name: such a trigger fires only while
-- session_replication_role is replica, which an ordinary session never is, and
-- the table's owner may set it without being a superuser.
CREATE OR REPLACE FUNCTION bb.data_protect_end() RETURNS event_trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, bb AS $$
DECLARE r record; bad text;
BEGIN
  FOR r IN SELECT * FROM pg_event_trigger_ddl_commands() LOOP
    IF r.object_type NOT IN ('table','trigger') OR r.schema_name IN ('bb','pg_catalog','information_schema')
       OR r.schema_name LIKE 'pg_temp%' THEN
      CONTINUE;
    END IF;
    IF r.object_type = 'table' AND r.command_tag IN ('CREATE TABLE','CREATE TABLE AS','SELECT INTO') THEN
      IF EXISTS (SELECT 1 FROM bb._data_tables() t WHERE t = r.objid::regclass) THEN
        PERFORM bb._attach_data_triggers(r.objid::regclass);
      END IF;
    END IF;
    -- ALTER TABLE … DISABLE TRIGGER or ENABLE REPLICA TRIGGER, or ALTER
    -- TRIGGER … RENAME, on one of ours. ENABLE ALWAYS stays allowed: it fires
    -- in more sessions, not fewer.
    IF r.command_tag IN ('ALTER TABLE','ALTER TRIGGER') AND NOT bb._override_in_force() THEN
      SELECT string_agg(t.tgname || ' on ' || t.tgrelid::regclass::text, ', ') INTO bad
      FROM pg_trigger t
      WHERE t.tgfoid IN ('bb.guard_truncate()'::regprocedure, 'bb.record_dml()'::regprocedure,
                         'bb.guard_dml()'::regprocedure, 'bb.guard_impact()'::regprocedure)
        AND (t.tgenabled IN ('D','R') OR t.tgname NOT IN ('bb_guard_truncate','bb_record_dml','bb_guard_dml','bb_guard_impact'))
        AND t.tgrelid = CASE WHEN r.object_type = 'table' THEN r.objid
                             ELSE (SELECT tgrelid FROM pg_trigger WHERE oid = r.objid) END;
      IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'guardrail: the Blackbox''s data-change triggers cannot be disabled or renamed, nor made replica-only (%)', bad
          USING ERRCODE = 'insufficient_privilege',
                HINT = 'Only superusers and members of db_admin may, with bb.allow_destructive=on.';
      END IF;
    END IF;
  END LOOP;
END;
$$;

-- sql_drop: refuse DROP TRIGGER on one of ours. A table's own drop takes its
-- triggers with it (they are not `original` then), which is fine.
CREATE OR REPLACE FUNCTION bb.data_protect_drop() RETURNS event_trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, bb AS $$
DECLARE r record;
BEGIN
  FOR r IN SELECT * FROM pg_event_trigger_dropped_objects() WHERE original AND object_type = 'trigger' LOOP
    IF r.object_identity ~ '^bb_(guard_truncate|record_dml|guard_dml|guard_impact) on ' AND NOT bb._override_in_force() THEN
      RAISE EXCEPTION 'guardrail: the Blackbox''s data-change trigger % cannot be dropped', r.object_identity
        USING ERRCODE = 'insufficient_privilege',
              HINT = 'Only superusers and members of db_admin may, with bb.allow_destructive=on.';
    END IF;
  END LOOP;
END;
$$;

-- No REVOKE here: ledger.sql grants every bb function to db_client on each
-- start, so a REVOKE would have to run on each start too, and a privilege
-- change after the event triggers are on is recorded as an unattributed
-- change (integration-v2 §9). Calling bb._attach_data_triggers only adds
-- these protective triggers, which is harmless.

-- Every existing table, now. (Creating these triggers is DDL; ledger.sql's
-- recorder skips the Blackbox's own triggers, so a start records nothing.)
SELECT bb._attach_data_triggers(t) FROM bb._data_tables() t;

-- Last, as in ledger.sql: nothing above should be seen by these.
DROP EVENT TRIGGER IF EXISTS bb_data_attach;
DROP EVENT TRIGGER IF EXISTS bb_data_protect_drop;
CREATE EVENT TRIGGER bb_data_attach ON ddl_command_end EXECUTE FUNCTION bb.data_protect_end();
CREATE EVENT TRIGGER bb_data_protect_drop ON sql_drop EXECUTE FUNCTION bb.data_protect_drop();
