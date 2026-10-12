package agentidentity

import (
	"crypto/sha256"
	"encoding/binary"
	"github.com/google/uuid"
)

// StepClaimID 将步骤尝试认领绑定原作业认领，取消与认领 ACK 丢失时仍能验证同一尝试
func StepClaimID(jobClaim, stepRunID string) string {
	digest := sha256.New()
	for _, value := range []string{"arcentra-job-step-claim-v1", jobClaim, stepRunID} {
		_, _ = digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = digest.Write([]byte(value))
	}
	return uuid.NewHash(sha256.New(), uuid.NameSpaceOID, digest.Sum(nil), 8).String()
}
