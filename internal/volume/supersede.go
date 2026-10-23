package volume

import (
	"errors"
	"sort"

	"github.com/midnight/midnight/internal/failurecodes"
)

// SupersedeCandidate is one in-flight deploy that wants to mount a
// volume in (ServiceID, EnvironmentID). Sequence is the monotonic
// `deployment_sequence` value (Lane 1's bigserial); the highest
// sequence wins.
type SupersedeCandidate struct {
	DeploymentID string
	Sequence     int64
}

// SupersedeOutcome describes the result of single-writer arbitration:
// exactly one Winner, every other candidate is in Losers with the
// typed reason DEPLOY_SUPERSEDED.
type SupersedeOutcome struct {
	Winner SupersedeCandidate
	Losers []SupersedeOutcomeLoss
}

// SupersedeOutcomeLoss is the per-loser record carrying the failure
// code and a stable reason string.
type SupersedeOutcomeLoss struct {
	Candidate SupersedeCandidate
	Code      failurecodes.Code
	Reason    string
}

// ErrNoSupersedeCandidates is returned by ArbitrateSupersede when
// the candidate list is empty.
var ErrNoSupersedeCandidates = errors.New("no supersede candidates")

// ArbitrateSupersede picks the highest-sequence candidate as the
// winner and marks the rest as DEPLOY_SUPERSEDED. Ties are broken by
// DeploymentID lexicographically so the result is deterministic across
// runs (panel-locked).
//
// The panel-locked semantics mirror Sprint 4 §S4-08: one ACTIVE,
// every other candidate transitions to SUPERSEDED with reason set.
func ArbitrateSupersede(candidates []SupersedeCandidate) (SupersedeOutcome, error) {
	if len(candidates) == 0 {
		return SupersedeOutcome{}, ErrNoSupersedeCandidates
	}
	sorted := make([]SupersedeCandidate, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Sequence != sorted[j].Sequence {
			return sorted[i].Sequence > sorted[j].Sequence
		}
		return sorted[i].DeploymentID < sorted[j].DeploymentID
	})
	winner := sorted[0]
	losers := make([]SupersedeOutcomeLoss, 0, len(sorted)-1)
	for _, c := range sorted[1:] {
		losers = append(losers, SupersedeOutcomeLoss{
			Candidate: c,
			Code:      failurecodes.DeploySuperseded,
			Reason:    "deployment superseded by a later sequence on the same (service, environment)",
		})
	}
	return SupersedeOutcome{Winner: winner, Losers: losers}, nil
}
