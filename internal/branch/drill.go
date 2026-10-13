// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/brand"
)

// A failover drill: end the primary without warning, recover, and report what
// it cost in writes and in seconds.
//
// Phase 6 of docs/FOX_HA_Plan.md, where the drill is the deliverable rather
// than the standby: "we fail over in about a minute" is a claim, and a dated
// report from a real failover is evidence. Section 5 of that plan says not to
// quote RPO and RTO figures until something measures them, which is this.
//
// Three things make it a drill rather than a demonstration.
//
// It kills the primary rather than stopping it. `docker stop` sends SIGTERM,
// Postgres does a fast shutdown, and a fast shutdown flushes its remaining WAL
// to the standby before it exits — so a stopped primary loses nothing and the
// number would be a flattering fiction. A machine that dies does not say
// goodbye, so the drill sends SIGKILL.
//
// It kills it with writes in flight. A drill run against an idle database
// measures an idle database.
//
// And it recovers through the real path: the measured time is HAFailover, the
// same function `fox ha failover` calls, not a promotion written for the test.

// DrillResult is one drill. The report is rendered from it, so the rendering is
// a pure function of the measurement and can be read in a test.
type DrillResult struct {
	At     time.Time `json:"at"`
	Writes int       `json:"writes"` // rows the drill tried to write

	// Acked is the highest row the server told the client was committed, and
	// Survived the highest of those that was on the promoted standby. The gap
	// is the only honest definition of data loss: a write the client was told
	// had happened, which then had not.
	Acked    int64 `json:"acked"`
	Survived int64 `json:"survived"`
	LostRows int64 `json:"lost_rows"`

	// WritesPerSecond turns rows into seconds, because "11 rows" means nothing
	// without the rate that produced them.
	WritesPerSecond float64 `json:"writes_per_second"`

	// TimeToService is from the kill to a write succeeding on the new primary.
	// It does not include noticing the failure or deciding to act: both are a
	// person today, which is what phase 3 is for, and a drill that pretended
	// otherwise would be the fiction this exists to replace.
	TimeToService time.Duration `json:"time_to_service"`

	// The configuration that produced the numbers. A drill measures one sample;
	// the configuration is what may be promised, and the two are reported side
	// by side so nobody quotes the sample.
	SyncMode       bool   `json:"sync_mode"`
	ArchiveTimeout int    `json:"archive_timeout_seconds"`
	Target         string `json:"target"`
	TargetRemote   bool   `json:"target_remote"`
	PGMajor        string `json:"pg_major"`

	// Notes carries anything that happened during the drill that the numbers do
	// not show, so a report is never quietly incomplete.
	Notes []string `json:"notes,omitempty"`

	// Ran is whether the drill got as far as ending the primary. A drill
	// refused before it started — no standby, already failed over — has nothing
	// to report, and printing a document of zeroes for it would be worse than
	// printing the refusal.
	Ran bool `json:"ran"`
}

// Measured reports whether there is anything worth writing down.
func (r DrillResult) Measured() bool { return r.Ran }

// DrillOptions are the knobs. Writes defaults to 400, which at the rate one
// psql session sustains is a few seconds of traffic — long enough to have
// writes in flight at the moment of the kill.
type DrillOptions struct {
	Writes   int
	Failback bool
}

// archiveTimeoutSeconds is how often the primary closes a WAL segment and ships
// it, and so the most a lost machine can cost once the archive is somewhere that
// machine's failure cannot reach. One constant because the drill report quotes
// it as the bound a buyer may be promised, and a report that disagreed with the
// setting would be worse than no report.
const archiveTimeoutSeconds = 60

// drillSchema is where the drill writes. A schema of its own rather than a
// table in public: it is obviously ours, it cannot collide with anything, and
// it goes away in one statement afterwards.
const drillSchema = "fox_drill"

// Drill runs one. It is destructive of service and not of data: the primary is
// killed and the standby promoted, which is what `fox ha failover` does on
// purpose, and every write the standby received survives.
func Drill(o DrillOptions) (DrillResult, error) {
	r := DrillResult{At: time.Now().UTC(), Writes: o.Writes}
	if r.Writes <= 0 {
		r.Writes = 400
	}
	if err := drillReady(); err != nil {
		return r, err
	}
	primary := PrimaryContainer()
	r.SyncMode = SyncActive()
	r.ArchiveTimeout = archiveTimeoutSeconds
	r.PGMajor = imageMajor(pgImage())
	if t, err := CurrentTarget(); err == nil {
		r.Target, r.TargetRemote = t.Describe(), t.Remote()
	}

	if err := drillPrepare(primary); err != nil {
		return r, err
	}

	// Writes in flight, then the machine dies under them. From here there has
	// been an outage, so whatever happens next is worth a report.
	r.Ran = true
	acked, rate, killedAt, err := drillWriteUntilKilled(primary, r.Writes)
	r.Acked, r.WritesPerSecond = acked, rate
	if err != nil {
		r.Notes = append(r.Notes, "the writer stopped early: "+err.Error())
	}
	if acked == 0 {
		return r, fmt.Errorf("the drill could not write anything to %s, so there is nothing to measure", primary)
	}

	// Recovery, through the command an operator would run.
	if err := HAFailover(); err != nil {
		return r, fmt.Errorf("the drill killed the primary and the failover failed: %w\n"+
			"The install is mid-failover; `%s ha status` says where it is", err, brand.CLI)
	}
	now := PrimaryContainer()
	if err := pollUntil("the promoted standby to accept a write", 2*time.Minute, func() bool {
		return drillWrite(now, -1) == nil
	}); err != nil {
		return r, err
	}
	r.TimeToService = time.Since(killedAt)

	// What arrived before the machine died.
	r.Survived = drillHighest(now)
	r.LostRows = r.Acked - r.Survived
	if r.LostRows < 0 {
		// The standby had rows the client was never told about: a commit that
		// reached the standby and whose acknowledgement never reached the
		// client. Not data loss — the opposite — but worth saying rather than
		// reporting a negative.
		r.Notes = append(r.Notes, fmt.Sprintf(
			"the promoted standby held %d row(s) beyond the last one acknowledged to the client, "+
				"which is a commit that arrived but whose reply did not", -r.LostRows))
		r.LostRows = 0
	}
	drillCleanup(now)

	if o.Failback {
		start := time.Now()
		if err := HAFailback(); err != nil {
			r.Notes = append(r.Notes, "the failback failed: "+err.Error())
		} else {
			r.Notes = append(r.Notes, fmt.Sprintf("returned to normal running in %s, and the standby "+
				"has to be rebuilt with `%s ha enable`", time.Since(start).Round(time.Second), brand.CLI))
		}
	}
	return r, nil
}

// drillReady refuses a drill that could not measure anything, before it kills
// anything.
func drillReady() error {
	if ContainerState("standby") == "absent" {
		return fmt.Errorf("a drill needs a standby to fail over to, and this install has none.\n"+
			"Run `%s ha enable` first", brand.CLI)
	}
	if PrimaryContainer() == container("standby") {
		return fmt.Errorf("'main' is already being served by the promoted standby since `%s ha failover`.\n"+
			"Run `%s ha failback` first, so the drill has somewhere to fail over to", brand.CLI, brand.CLI)
	}
	if !HAInfo().Streaming {
		return fmt.Errorf("the standby is not streaming, so a drill would measure a failover to a stale copy.\n"+
			"Check `%s ha status` and run the drill once it is caught up", brand.CLI)
	}
	return nil
}

func drillPrepare(primary string) error {
	// Created, never dropped, and emptied with a WHERE clause.
	//
	// The engine's own guardrails refuse DROP TABLE and unqualified DML by
	// default — the two that stay free in every edition — and they refused this
	// when it was written as a DROP, which is the guardrails working. A drill
	// does not get an exemption from the product's own rules, so preparing
	// needs none: the one place that overrides anything is the cleanup at the
	// end, and it says so.
	out, err := captureCombined("docker", "exec", "--env-file", pgEnvFile(), primary,
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAq", "-v", "ON_ERROR_STOP=1",
		"-c", "CREATE SCHEMA IF NOT EXISTS "+drillSchema,
		"-c", "CREATE TABLE IF NOT EXISTS "+drillSchema+".log (id bigint primary key, at timestamptz default now())",
		"-c", "DELETE FROM "+drillSchema+".log WHERE id IS NOT NULL")
	if err != nil {
		return fmt.Errorf("preparing the drill table: %w\n%s", err, strings.TrimSpace(out))
	}
	return nil
}

// drillWriteUntilKilled writes one row per transaction through a single session,
// reads back what the server acknowledged, and kills the primary part way
// through so that writes are in flight when it dies.
//
// One session fed statement by statement rather than a batch: the point is the
// client's own view of what was committed, and a batch would only tell us about
// the last one. It returns the highest acknowledged row, the rate it managed,
// and the instant the kill was sent — which is where the clock for recovery
// starts.
func drillWriteUntilKilled(primary string, writes int) (int64, float64, time.Time, error) {
	cmd, _, cancel := sudoCmd(execLong, "docker", "exec", "-i", "--env-file", pgEnvFile(), primary,
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAq", "-v", "ON_ERROR_STOP=1")
	defer cancel()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, 0, time.Now(), err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, time.Now(), err
	}
	if err := cmd.Start(); err != nil {
		return 0, 0, time.Now(), err
	}
	defer func() { _ = cmd.Wait() }()

	// Killed at the half-way mark, so the second half is what the failure
	// interrupts.
	killAfter := writes / 2
	var acked int64
	killedAt := time.Time{}
	start := time.Now()
	lines := bufio.NewScanner(stdout)
	for i := 1; i <= writes; i++ {
		if _, err := fmt.Fprintf(stdin, "INSERT INTO %s.log (id) VALUES (%d) RETURNING %d;\n",
			drillSchema, i, i); err != nil {
			break // the session is gone, which after the kill is expected
		}
		if !lines.Scan() {
			break
		}
		if strings.TrimSpace(lines.Text()) != fmt.Sprint(i) {
			break
		}
		acked = int64(i)
		if i == killAfter {
			killedAt = time.Now()
			// SIGKILL: a machine that dies does not flush its WAL to the
			// standby on the way out, and `docker stop` would.
			quiet("docker", "kill", "-s", "KILL", primary)
		}
	}
	_ = stdin.Close()
	if killedAt.IsZero() {
		// Fewer writes got through than the half-way mark, so nothing has been
		// killed yet and the drill still has to end the primary.
		killedAt = time.Now()
		quiet("docker", "kill", "-s", "KILL", primary)
	}
	rate := 0.0
	if d := killedAt.Sub(start).Seconds(); d > 0 {
		rate = float64(acked) / d
	}
	return acked, rate, killedAt, nil
}

// drillWrite writes one row, and is how "is it serving again" is asked: a
// read would be answered by a standby still in recovery.
func drillWrite(server string, id int64) error {
	_, err := captureCombined("docker", "exec", "--env-file", pgEnvFile(), server,
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAq", "-v", "ON_ERROR_STOP=1", "-c",
		fmt.Sprintf("INSERT INTO %s.log (id) VALUES (%d) ON CONFLICT DO NOTHING", drillSchema, id))
	return err
}

func drillHighest(server string) int64 {
	out, err := capture("docker", "exec", "--env-file", pgEnvFile(), server,
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAc",
		"SELECT coalesce(max(id), 0) FROM "+drillSchema+".log WHERE id > 0")
	if err != nil {
		return 0
	}
	var n int64
	if _, err := fmt.Sscan(strings.TrimSpace(out), &n); err != nil {
		return 0
	}
	return n
}

// drillCleanup removes the schema the drill wrote, which needs the same
// override a person would use: dropping a table is one of the two things the
// Blackbox guardrails refuse by default, and a drill does not get an exemption
// from the product's own rules.
func drillCleanup(server string) {
	quiet("docker", "exec", "--env-file", pgEnvFile(), server,
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAq", "-c",
		"SET bb.allow_destructive=on; DROP SCHEMA IF EXISTS "+drillSchema+" CASCADE")
}

// Report renders the drill as a dated document, which is the deliverable.
//
// Pure, so what it says can be read in a test rather than inferred from a run.
// The shape is deliberate: what was measured, then what may be promised, then
// what was not tested. The third section exists because the first is the one
// that gets quoted, and a single-machine drill is not evidence of surviving a
// site.
func (r DrillResult) Report() string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }

	p("# Failover drill — %s\n\n", r.At.Format(time.RFC3339))
	p("A real failover: the primary was killed with SIGKILL half way through %d writes, with\n", r.Writes)
	p("more still arriving, and the standby was promoted through `%s ha failover`.\n\n", brand.CLI)

	p("## Measured\n\n")
	p("| | |\n|---|---|\n")
	p("| Writes acknowledged to the client | %d, at %.0f/s |\n", r.Acked, r.WritesPerSecond)
	p("| Acknowledged writes lost (RPO) | **%d** |\n", r.LostRows)
	if r.LostRows > 0 && r.WritesPerSecond > 0 {
		p("| …which at that rate is | **%.1fs** of writes |\n",
			float64(r.LostRows)/r.WritesPerSecond)
	}
	p("| Kill to a write on the new primary (RTO) | **%s** |\n", r.TimeToService.Round(100*time.Millisecond))
	p("\nRTO above starts at the kill. It does not include noticing the failure or deciding\n")
	p("to fail over, both of which are a person today.\n\n")

	p("## The configuration that produced those numbers\n\n")
	p("| | |\n|---|---|\n")
	if r.SyncMode {
		p("| Durability | zero-data-loss mode **on**: a commit waits for the standby |\n")
	} else {
		p("| Durability | asynchronous: commits never wait for the standby |\n")
	}
	p("| WAL archived at least every | %ds |\n", r.ArchiveTimeout)
	p("| Backups and WAL go to | %s |\n", r.Target)
	p("| PostgreSQL | %s |\n", r.PGMajor)
	p("\n")

	p("## What may be quoted\n\n")
	if r.SyncMode {
		p("**RPO zero**, and it is a property of the configuration rather than of this sample:\n")
		p("with zero-data-loss mode on, a commit is not acknowledged until the standby has it,\n")
		p("so there is no window in which an acknowledged write can be lost. The %d above is a\n", r.LostRows)
		p("confirmation, not the evidence.\n\n")
	} else if r.LostRows == 0 {
		// The dangerous result. Nothing was lost, and nothing about the
		// configuration promises that — on one host with a standby microseconds
		// behind it is the usual outcome, and it is the number somebody will
		// want to put in a tender.
		p("**Nothing, on the strength of this.** Nothing was lost, and asynchronous replication\n")
		p("does not promise that: the standby was simply keeping up at %.0f writes/s on a machine\n", r.WritesPerSecond)
		p("it shares with the primary. A busier primary, a slower link or a standby doing anything\n")
		p("else would all lose writes, and nothing in this configuration bounds how many.\n\n")
		p("What can be promised is the archive: WAL is shipped at least every %ds, so a lost\n", r.ArchiveTimeout)
		p("machine costs at most that much once the archive is somewhere the machine's failure\n")
		p("cannot reach. And `%s ha sync on` makes zero a guarantee rather than an observation,\n", brand.CLI)
		p("at the cost of making every commit wait for the standby.\n\n")
	} else {
		p("**Not the number above.** Asynchronous replication has no bound on how far the standby\n")
		p("may be behind, so %d lost row(s) is what this workload cost at %.0f writes/s on this\n", r.LostRows, r.WritesPerSecond)
		p("machine, and a busier primary or a slower link would lose more. What can be promised\n")
		p("is the archive: WAL is shipped at least every %ds, so a lost machine costs at most that\n", r.ArchiveTimeout)
		p("much once the archive is somewhere the machine's failure cannot reach.\n\n")
		p("`%s ha sync on` turns that number into a guarantee of zero, at the cost of making every\n", brand.CLI)
		p("commit wait for the standby.\n\n")
	}
	if !r.TargetRemote {
		p("**The archive is on this machine.** One disk or host failure takes the database and\n")
		p("every backup of it at the same time, so nothing above is a disaster-recovery figure\n")
		p("until `%s backup target set` points it somewhere else.\n\n", brand.CLI)
	}

	p("## What this drill did not test\n\n")
	p("- **Losing a machine.** The standby is a container on this host, sharing its disk and\n")
	p("  its power. This measures promotion and rerouting, not survival of the host.\n")
	p("- **Noticing.** Detection and the decision to fail over were manual.\n")
	p("- **A second site.** There is only one.\n")
	p("\nThe first and third are what phase 1 of the HA plan is for, and the second phase 3.\n")
	if len(r.Notes) > 0 {
		p("\n## Notes\n\n")
		for _, n := range r.Notes {
			p("- %s\n", n)
		}
	}
	return b.String()
}

// DrillReportDir is where reports are kept: in the state directory, beside the
// licence and the primary pointer, because a drill report is a record of the
// install rather than a document somebody happened to produce.
func DrillReportDir() string { return brand.StatePath("drills") }

// SaveDrillReport writes the report and returns where it went.
//
// Named for the instant the drill began rather than for when it finished, so a
// report sorts by the event it describes.
func SaveDrillReport(r DrillResult) (string, error) {
	dir := DrillReportDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, r.At.Format("20060102T150405Z")+".md")
	if err := os.WriteFile(path, []byte(r.Report()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
