package knowledge

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const KnowledgeQueue = "stella_knowledge"

var activeKnowledgeJobStates = []rivertype.JobState{
	rivertype.JobStateAvailable,
	rivertype.JobStatePending,
	rivertype.JobStateRunning,
	rivertype.JobStateScheduled,
	rivertype.JobStateRetryable,
}

// chunkArgs is the complete PR-1 queue contract. The worker deliberately lives
// in the derivation/lifecycle slice; this payload carries only durable identity.
type chunkArgs struct {
	FileID string `json:"file_id" river:"unique"`
}

func (chunkArgs) Kind() string { return "stella_knowledge_chunk" }

func (chunkArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       KnowledgeQueue,
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: append([]rivertype.JobState(nil), activeKnowledgeJobStates...),
		},
	}
}
