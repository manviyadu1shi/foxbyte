// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/brand"
)

// The third opinion, without which automatic failover is a way to produce two
// primaries — phase 3 of docs/FOX_HA_Plan.md.
//
// fenceRemotePrimary already says what the gap is: "it cannot cover a network
// partition: if this machine cannot reach the primary but clients still can,
// promoting here produces exactly the split-brain above and no amount of local
// probing can tell the two situations apart." Two machines cannot answer that
// question about each other. Somebody else has to look.
//
// So this is the monitor node the plan asked for, in the smallest form that
// answers the question: a process on a third host that replies to exactly one
// question — can *it* reach the primary? The standby promotes only when both
// opinions agree that the primary is gone. One opinion is not enough and three
// is a different product.
//
// Why not pg_auto_failover or Patroni, which the plan leaned towards. Both want
// to own the cluster: they create the data directory, write the recovery
// configuration and perform the promotion. This engine owns all three —
// branches are ZFS clones of a data directory it made, `node join` writes
// primary_conninfo, and HAFailover promotes behind a fence it checks itself.
// Marrying either one would mean handing over the parts branching is built on.
// There is a second reason, measured when fencing landed and recorded in §6.3
// of the plan: pg_auto_failover installs for PostgreSQL 18 from pgdg, but its
// last tagged release is April 2025 and the PG 17/18/19 work is untagged on
// `main` — a dependency whose maintenance we would be betting the failover on.
// What those projects are actually for is the quorum, and that is the part
// reproduced here: a witness, and a refusal without one.
//
// What this does not do, said plainly rather than implied:
//
//   - It is not consensus. Two observers agreeing is not a majority of three
//     voters; it is two independent observations. A witness that is itself
//     partitioned from the primary while clients are not would agree with the
//     standby and the promotion would be wrong. Placing the witness where the
//     clients are is therefore the operator's decision and the one that
//     matters, which is why `ha watch` prints it rather than assuming it.
//   - It carries no authority over the data. A forged witness answer cannot by
//     itself promote anything: the standby must independently have failed to
//     reach the primary. An attacker who can both cut that link and forge the
//     answer can cause a split brain, and the token below is not a defence
//     against that — TLS between the nodes is, and it is the follow-up.

// WitnessPath is the one question a witness answers.
const WitnessPath = "/ha/primary"

// WitnessAnswer is what it says. The witness names itself so a report, or a
// log line after an incident, says who gave the second opinion.
type WitnessAnswer struct {
	Primary   string `json:"primary"`
	Reachable bool   `json:"reachable"`
	At        string `json:"at"`
	Witness   string `json:"witness"`
	Error     string `json:"error,omitempty"`
}

// witnessProbeTimeout is how long the witness waits for the primary. Short: a
// primary that takes three seconds to accept a connection is not a primary
// anybody is being served by, and the watcher is already counting consecutive
// failures rather than acting on one.
const witnessProbeTimeout = 3 * time.Second

// primaryAnswering reports whether something accepts a connection at an
// address. The same question RemotePrimaryAnswering asks of the join record,
// asked of an address a witness was given — and deliberately no more than that:
// a witness holds no credential of ours and needs none to say whether the
// machine is answering at all.
func primaryAnswering(addr string, timeout time.Duration) (bool, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("%q is not host:port", addr)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return false, fmt.Errorf("%q has no port number", addr)
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return false, nil
	}
	_ = c.Close()
	return true, nil
}

// WitnessHandler is the witness as an http.Handler, so what it answers can be
// read in a test rather than against a running host.
func WitnessHandler(primary, token string) http.Handler {
	mux := http.NewServeMux()
	me, _ := os.Hostname()
	mux.HandleFunc(WitnessPath, func(w http.ResponseWriter, r *http.Request) {
		if token != "" && !bearerIs(r.Header.Get("Authorization"), token) {
			// 404 rather than 401: a witness that confirms it is a witness to
			// anybody who asks is a map of somebody's database cluster.
			http.NotFound(w, r)
			return
		}
		up, err := primaryAnswering(primary, witnessProbeTimeout)
		a := WitnessAnswer{
			Primary:   primary,
			Reachable: up,
			At:        time.Now().UTC().Format(time.RFC3339),
			Witness:   me,
		}
		if err != nil {
			a.Error = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a)
	})
	return mux
}

func bearerIs(header, token string) bool {
	got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), "Bearer"))
	return got == token
}

// ServeWitness runs the witness until the process is stopped.
func ServeWitness(addr, primary, token string) error {
	if _, _, err := net.SplitHostPort(primary); err != nil {
		return fmt.Errorf("--primary must be host:port (the primary's Postgres address): %w", err)
	}
	if token == "" {
		fmt.Printf("WARNING: no --token, so anything that can reach %s can ask this witness\n"+
			"whether %s is up. It answers nothing else and holds no credential, but a token\n"+
			"also stops two clusters pointing at one witness by accident.\n\n", addr, primary)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           WitnessHandler(primary, token),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("witness for %s on http://%s%s\n", primary, addr, WitnessPath)
	fmt.Printf("point a standby at it with:\n  %s ha watch --witness http://%s%s\n\n", brand.CLI, addr, WitnessPath)
	fmt.Println("Put this somewhere the clients are, not beside either database: its whole job is")
	fmt.Println("to say whether the primary is reachable from where the work is.")
	return srv.ListenAndServe()
}

// witnessView is what the second opinion amounts to this round.
type witnessView int

const (
	witnessSilent      witnessView = iota // it could not be asked
	witnessPrimaryUp                      // it reaches the primary
	witnessPrimaryDown                    // it does not either
)

func (v witnessView) String() string {
	switch v {
	case witnessPrimaryUp:
		return "the witness reaches the primary"
	case witnessPrimaryDown:
		return "the witness cannot reach the primary either"
	}
	return "the witness could not be asked"
}

// AskWitness puts the question. A witness that cannot be reached is silent
// rather than agreeing: the whole point is that a promotion needs two
// opinions, and an absent one is not an opinion.
func AskWitness(url, token string, timeout time.Duration) (witnessView, WitnessAnswer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return witnessSilent, WitnessAnswer{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return witnessSilent, WitnessAnswer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return witnessSilent, WitnessAnswer{}, fmt.Errorf("the witness answered %s — check the URL and the token",
			resp.Status)
	}
	var a WitnessAnswer
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return witnessSilent, WitnessAnswer{}, fmt.Errorf("the witness's answer was not readable: %w", err)
	}
	if a.Reachable {
		return witnessPrimaryUp, a, nil
	}
	return witnessPrimaryDown, a, nil
}

// witnessAgreesPrimaryIsGone is the only combination that may promote, written
// once so the loop and the log cannot disagree about it.
func witnessAgreesPrimaryIsGone(v witnessView) bool { return v == witnessPrimaryDown }
