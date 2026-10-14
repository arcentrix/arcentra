package agentidentity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"google.golang.org/protobuf/proto"
)

// TerminalReceiptDigest 兼容既有持久回执算法，绑定完整线载荷、原认领及执行范围
func TerminalReceiptDigest(request proto.Message, claim string, executionScope ...string) (string, error) {
	if request == nil || !request.ProtoReflect().IsValid() {
		return "", fmt.Errorf("terminal request is required")
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(proto.Clone(request))
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	for _, value := range append([]string{"arcentra-terminal-report-v1", string(request.ProtoReflect().Descriptor().FullName()), claim}, executionScope...) {
		_, _ = digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = digest.Write([]byte(value))
	}
	_, _ = digest.Write(encoded)
	return hex.EncodeToString(digest.Sum(nil)), nil
}
