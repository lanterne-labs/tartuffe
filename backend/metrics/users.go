package metrics

import "github.com/prometheus/client_golang/prometheus"

// The users' side of the site: sign-ins, identity verifications, votes,
// likes and reports (dashboard "Users"). The totals live in the database;
// these counters give the rates and the refusals, which the database does
// not keep.
var (
	// AuthChallengesTotal counts the one-time challenges issued: a sign-in or
	// a verification started.
	AuthChallengesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "auth_challenges_total",
		Help:      "One-time challenges issued, by provider (a sign-in or a verification started)",
	}, []string{"provider"})

	// AuthSignInsTotal counts the sign-ins by provider and outcome (ok,
	// refused: a bad or expired proof, error).
	AuthSignInsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "auth_signins_total",
		Help:      "Sign-ins by provider and outcome (ok, refused, error)",
	}, []string{"provider", "outcome"})

	// VerificationsTotal counts the document proofs checked by provider and
	// outcome (ok, refused, error).
	VerificationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "verifications_total",
		Help:      "Document proofs checked by provider and outcome (ok, refused, error)",
	}, []string{"provider", "outcome"})

	// BallotChangesTotal counts the anonymous ballots cast by outcome (ok,
	// or the code of the refusal: closed, invalid_ballot, stale_seq,
	// unknown_root).
	BallotChangesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "ballot_changes_total",
		Help:      "Ballot changes by outcome (ok or the refusal code)",
	}, []string{"outcome"})

	// VoterRegistrationsTotal counts the voting keys registered by provider
	// and outcome (registered, replaced, already, taken, invalid).
	VoterRegistrationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "voter_registrations_total",
		Help:      "Voting keys registered by provider and outcome (registered, replaced, already, taken, invalid)",
	}, []string{"provider", "outcome"})

	// FeedLikesTotal counts the +1 given and taken back, by item kind.
	FeedLikesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "feed_likes_total",
		Help:      "+1 given (like) and taken back (unlike), by item kind",
	}, []string{"kind", "action"})

	// UserReportsTotal counts the reports by item kind and outcome (created
	// or the refusal code).
	UserReportsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "user_reports_total",
		Help:      "Users' reports by item kind and outcome (created or the refusal code)",
	}, []string{"kind", "outcome"})

	// AccountsDeletedTotal counts the accounts their users deleted.
	AccountsDeletedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "accounts_deleted_total",
		Help:      "Accounts deleted by their users",
	})
)
