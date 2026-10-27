package cmd

import (
	"fmt"
	"strings"
	"time"

	"digital-mirror/backend/vote"

	"github.com/spf13/cobra"
)

var voteRelaysCmd = &cobra.Command{
	Use:   "relays",
	Short: "The relays of a vote: measure candidates against SPEC §7.1",
}

var voteRelaysProbeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Measure relays: NIP-11, a ballot-sized post, a server post, and the retention of earlier probes",
	Long: `For each relay: what it says of itself (NIP-11: size limit, auth, payment,
proof of work, retention), whether it takes a ballot-sized voter event and a
server event of our kinds now, and whether it still serves the events earlier
runs posted (--state keeps them). Run it today, then again in a day, a week,
a month: the retention a relay really has is what the post-hoc withholding
check (SPEC §12.6) rests on.`,
	Example:      `  dm vote relays probe --state relays-probe.json --relays wss://nos.lol,wss://relay.primal.net`,
	RunE:         runVoteRelaysProbe,
	SilenceUsage: true,
}

// candidateRelays are public relays worth measuring (different operators
// where known); --relays replaces the list.
var candidateRelays = []string{
	"wss://nos.lol", "wss://relay.damus.io", "wss://relay.primal.net", "wss://nostr.mom", "wss://relay.nostr.band",
	"wss://relay.snort.social", "wss://nostr.wine", "wss://nostr.oxtr.dev", "wss://offchain.pub", "wss://relay.nostr.bg",
	"wss://nostr.bitcoiner.social", "wss://relay.nostr.net", "wss://nostr-pub.wellorder.net", "wss://nostr.einundzwanzig.space",
	"wss://nostr.land", "wss://relay.0xchat.com", "wss://nostr21.com", "wss://relay.nostromo.social", "wss://nostr.fmt.wiz.biz",
	"wss://relay.nostr.wirednet.jp", "wss://nostr.sathoarder.com", "wss://relay.nostrplebs.com",
}

func init() {
	voteCmd.AddCommand(voteRelaysCmd)
	voteRelaysCmd.AddCommand(voteRelaysProbeCmd)
	f := voteRelaysProbeCmd.Flags()
	f.String("state", "relays-probe.json", "the file of the events posted by earlier runs")
	f.String("relays", "", "wss:// URLs, comma-separated (default: a list of public relays)")
}

func runVoteRelaysProbe(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()
	statePath, _ := f.GetString("state")
	relayList, _ := f.GetString("relays")
	relays := candidateRelays
	if relayList != "" {
		relays = nil
		for _, u := range strings.Split(relayList, ",") {
			if u = strings.TrimSpace(u); u != "" {
				relays = append(relays, u)
			}
		}
	}
	st, err := vote.LoadProbeState(statePath)
	if err != nil {
		return err
	}
	key, err := vote.NostrKeyFromEnv()
	if err != nil {
		// The probe's server key need not be the vote's: a fixed one of its own.
		key, _ = vote.NewNostrKey("7a0b3e4c5d6f7081920a3b4c5d6e7f80919293a4b5c6d7e8f90a1b2c3d4e5f60")
	}
	results := vote.ProbeRelays(ctx, relays, st, key)
	if err := st.Save(statePath); err != nil {
		return err
	}
	days := func(d time.Duration) string {
		if d == 0 {
			return "-"
		}
		if d < 48*time.Hour {
			return fmt.Sprintf("%.0fh", d.Hours())
		}
		return fmt.Sprintf("%.0fd", d.Hours()/24)
	}
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	fmt.Printf("%-34s %-28s %7s %4s %4s %3s  %-7s %-7s %5s %5s %7s %7s  %s\n", "relay", "software", "maxKB", "auth", "pay", "pow", "ballot", "server", "kept", "lost", "oldest", "lost<", "retention / error")
	for _, r := range results {
		software := r.Software
		if len(software) > 28 {
			software = software[len(software)-28:]
		}
		maxKB := "-"
		if r.MaxMessage > 0 {
			maxKB = fmt.Sprintf("%d", r.MaxMessage/1024)
		}
		post := func(s string) string {
			if s == "" {
				return "ok"
			}
			return "REFUSED"
		}
		note := r.Retention
		if r.InfoErr != "" {
			note = "nip11: " + firstLine(r.InfoErr)
		}
		if r.BallotPost != "" {
			note += " | ballot: " + firstLine(r.BallotPost)
		}
		if r.ServerPost != "" && r.ServerPost != r.BallotPost {
			note += " | server: " + firstLine(r.ServerPost)
		}
		fmt.Printf("%-34s %-28s %7s %4s %4s %3d  %-7s %-7s %5d %5d %7s %7s  %s\n", r.Relay, software, maxKB, yes(r.Auth), yes(r.Payment), r.MinPow,
			post(r.BallotPost), post(r.ServerPost), r.Kept, r.Lost, days(r.OldestKept), days(r.YoungestLost), note)
	}
	fmt.Printf("\n%d events posted in all; run again later to measure the retention.\n", len(st.Posted))
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 90 {
		s = s[:90]
	}
	return s
}
