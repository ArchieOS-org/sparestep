package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	blockerStatusFailed    = "failed"
	blockerStatusUnknown   = "unknown"
	blockerStatusSucceeded = "succeeded"
	blockerStatusClaimed   = "claimed"

	maxBlockerObligations = 64
	emptyCWDProjectKey    = "<empty-cwd>"
)

// blockerLedger records delivery obligations independently of a turn's normal
// tally. OperationKey is deliberately opaque: callers must provide a stable
// fingerprint or one of the native delivery keys, never a command or response.
type blockerLedger struct {
	SessionID        string              `json:"session_id"`
	ProjectKey       string              `json:"project_key"`
	Obligations      []blockerObligation `json:"obligations"`
	Recoveries       int                 `json:"recoveries"`
	LastRecoveryTurn string              `json:"last_recovery_turn,omitempty"`
	LastDeliveryTime int64               `json:"last_delivery_time,omitempty"`
	LastDeliveryID   string              `json:"last_delivery_id,omitempty"`
}

type blockerObligation struct {
	Kind               string `json:"kind"`
	OperationKey       string `json:"operation_key"`
	Status             string `json:"status"`
	OriginTurn         string `json:"origin_turn"`
	Failed             bool   `json:"failed"`
	ChallengeAttempted bool   `json:"challenge_attempted"`
}

type blockerLedgerSnapshot struct {
	Unresolved       int    `json:"unresolved"`
	Unsupported      int    `json:"unsupported"`
	Recoveries       int    `json:"recoveries"`
	LastRecoveryTurn string `json:"last_recovery_turn,omitempty"`
}

func (l blockerLedger) unresolvedCount() int { return len(l.Obligations) }

func (l blockerLedger) unsupportedCount() int {
	unsupported := 0
	for _, obligation := range l.Obligations {
		// A terminal claim and an unknown result both lack a verified failed
		// delivery result. They remain unsupported until a matching success or
		// an explicit failure supplies evidence.
		if !obligation.Failed {
			unsupported++
		}
	}
	return unsupported
}

func (l blockerLedger) snapshot() blockerLedgerSnapshot {
	return blockerLedgerSnapshot{
		Unresolved:       l.unresolvedCount(),
		Unsupported:      l.unsupportedCount(),
		Recoveries:       l.Recoveries,
		LastRecoveryTurn: l.LastRecoveryTurn,
	}
}

// ledgerLoad returns the durable ledger for this session and canonical project.
func ledgerLoad(e event) (blockerLedger, error) {
	path, projectKey, err := blockerLedgerPath(e)
	if err != nil {
		return blockerLedger{}, err
	}
	ledger, err := loadBlockerLedger(path, e.SessionID, projectKey)
	if err != nil {
		return blockerLedger{}, err
	}
	return ledger, nil
}

// ledgerRecord records an observed terminal result or a terminal blocker claim.
// A success clears only the same kind/key, except for native deploy, which can
// also discharge the native ship obligation it proves complete.
func ledgerRecord(e event, operationKey, kind, status string) (blockerLedger, error) {
	if err := validateBlockerRecord(operationKey, kind, status); err != nil {
		return blockerLedger{}, err
	}
	path, projectKey, err := blockerLedgerPath(e)
	if err != nil {
		return blockerLedger{}, err
	}
	unlock, err := acquireStateLock(path)
	if err != nil {
		return blockerLedger{}, err
	}
	defer func() { _ = unlock() }()

	ledger, err := loadBlockerLedger(path, e.SessionID, projectKey)
	if err != nil {
		return blockerLedger{}, err
	}
	if e.HookEventName == "DeliveryResult" {
		stamp, _, _ := strings.Cut(e.DeliveryAttemptID, "-")
		nanoseconds, _ := strconv.ParseInt(stamp, 10, 64)
		if nanoseconds > 0 && nanoseconds <= ledger.LastDeliveryTime {
			return ledger, nil
		}
		ledger.LastDeliveryTime, ledger.LastDeliveryID = nanoseconds, e.DeliveryAttemptID
	}
	if status == blockerStatusSucceeded {
		ledger.resolve(kind, operationKey, e.TurnID)
		if kind == "deploy" && operationKey == "deploy" {
			ledger.resolveNativeDeployDependencies(e.TurnID)
		}
	} else {
		if err := ledger.addOrUpdate(kind, operationKey, status, e.TurnID); err != nil {
			return blockerLedger{}, err
		}
	}
	if err := saveBlockerLedger(path, ledger); err != nil {
		return blockerLedger{}, err
	}
	return ledger, nil
}

// ledgerChallenge atomically consumes the one reminder allowed for every
// currently unresolved episode. A later, genuinely new obligation can prompt
// once again after this call.
func ledgerChallenge(e event) (blockerLedger, bool, error) {
	path, projectKey, err := blockerLedgerPath(e)
	if err != nil {
		return blockerLedger{}, false, err
	}
	unlock, err := acquireStateLock(path)
	if err != nil {
		return blockerLedger{}, false, err
	}
	defer func() { _ = unlock() }()

	ledger, err := loadBlockerLedger(path, e.SessionID, projectKey)
	if err != nil {
		return blockerLedger{}, false, err
	}
	shouldChallenge := false
	for index := range ledger.Obligations {
		if !ledger.Obligations[index].ChallengeAttempted {
			ledger.Obligations[index].ChallengeAttempted = true
			shouldChallenge = true
		}
	}
	if shouldChallenge {
		if err := saveBlockerLedger(path, ledger); err != nil {
			return blockerLedger{}, false, err
		}
	}
	return ledger, shouldChallenge, nil
}

func (l *blockerLedger) addOrUpdate(kind, operationKey, status, turnID string) error {
	for index := range l.Obligations {
		obligation := &l.Obligations[index]
		if obligation.Kind != kind || obligation.OperationKey != operationKey {
			continue
		}
		obligation.Status = status
		if status == blockerStatusFailed {
			obligation.Failed = true
		}
		return nil
	}
	if len(l.Obligations) >= maxBlockerObligations {
		return fmt.Errorf("blocker ledger has %d unresolved obligations", len(l.Obligations))
	}
	l.Obligations = append(l.Obligations, blockerObligation{
		Kind:         kind,
		OperationKey: operationKey,
		Status:       status,
		OriginTurn:   turnID,
		Failed:       status == blockerStatusFailed,
	})
	return nil
}

func (l *blockerLedger) resolve(kind, operationKey, turnID string) {
	l.resolveWhere(turnID, func(obligation blockerObligation) bool {
		return obligation.Kind == kind && obligation.OperationKey == operationKey
	})
}

func (l *blockerLedger) resolveNativeDeployDependencies(turnID string) {
	l.resolveWhere(turnID, func(obligation blockerObligation) bool {
		if obligation.Kind == "deploy" && obligation.OperationKey == "deploy" {
			return obligation.Status == blockerStatusClaimed
		}
		return obligation.Kind == "ship" && obligation.OperationKey == "ship" &&
			(obligation.Status == blockerStatusClaimed || obligation.Failed)
	})
}

func (l *blockerLedger) resolveWhere(turnID string, matches func(blockerObligation) bool) {
	kept := l.Obligations[:0]
	for _, obligation := range l.Obligations {
		if !matches(obligation) {
			kept = append(kept, obligation)
			continue
		}
		if obligation.Failed {
			l.Recoveries++
			l.LastRecoveryTurn = turnID
		}
	}
	l.Obligations = kept
}

func validateBlockerRecord(operationKey, kind, status string) error {
	if strings.TrimSpace(operationKey) == "" || strings.TrimSpace(kind) == "" {
		return errors.New("blocker ledger requires an operation key and kind")
	}
	switch kind {
	case "ship", "deploy", "work":
	default:
		return fmt.Errorf("unsupported blocker kind %q", kind)
	}
	switch status {
	case blockerStatusFailed, blockerStatusUnknown, blockerStatusSucceeded, blockerStatusClaimed:
		return nil
	default:
		return fmt.Errorf("unsupported blocker status %q", status)
	}
}

func blockerLedgerPath(e event) (string, string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", "", err
	}
	projectKey := canonicalProjectKey(e.CWD)
	sum := sha256.Sum256([]byte(e.SessionID + ":" + projectKey))
	return filepath.Join(dir, "blockers-"+hex.EncodeToString(sum[:12])+".json"), projectKey, nil
}

func canonicalProjectKey(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return emptyCWDProjectKey
	}
	cleaned, err := filepath.Abs(cwd)
	if err != nil {
		cleaned = filepath.Clean(cwd)
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(cleaned)
}

func loadBlockerLedger(path, sessionID, projectKey string) (blockerLedger, error) {
	ledger := blockerLedger{SessionID: sessionID, ProjectKey: projectKey, Obligations: []blockerObligation{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return blockerLedger{}, err
	}
	if err := json.Unmarshal(b, &ledger); err != nil {
		return blockerLedger{}, fmt.Errorf("read blocker ledger: %w", err)
	}
	if ledger.SessionID != sessionID || ledger.ProjectKey != projectKey {
		return blockerLedger{}, errors.New("blocker ledger scope mismatch")
	}
	if ledger.Obligations == nil {
		ledger.Obligations = []blockerObligation{}
	}
	if len(ledger.Obligations) > maxBlockerObligations {
		return blockerLedger{}, fmt.Errorf("blocker ledger has too many unresolved obligations")
	}
	return ledger, nil
}

func saveBlockerLedger(path string, ledger blockerLedger) error {
	b, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
