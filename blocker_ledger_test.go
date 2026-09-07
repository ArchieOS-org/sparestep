package main

import (
	"os"
	"path/filepath"
	"testing"
)

func blockerLedgerTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "one-shot-tally-blocker-ledger-")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SHIP_IT_KEEP_TEST_DIRS") == "" {
		t.Cleanup(func() {
			if err := os.RemoveAll(dir); err != nil {
				t.Errorf("cleanup %s: %v", dir, err)
			}
		})
	}
	return dir
}

func TestBlockerLedgerFailedDeploySurvivesOrdinaryWorkAndShip(t *testing.T) {
	dir := blockerLedgerTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", dir)
	e := event{SessionID: "session", TurnID: "failed", CWD: blockerLedgerTestDir(t)}
	if _, err := ledgerRecord(e, "deploy", "deploy", blockerStatusFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := ledgerRecord(event{SessionID: e.SessionID, TurnID: "test", CWD: e.CWD}, "check", "work", blockerStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	ledger, err := ledgerRecord(event{SessionID: e.SessionID, TurnID: "ship", CWD: e.CWD}, "ship", "ship", blockerStatusSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.snapshot(); got.Unresolved != 1 || got.Recoveries != 0 {
		t.Fatalf("ordinary work or ship cleared deploy failure: %#v", got)
	}
}

func TestBlockerLedgerMatchedDeployRecoveryIsCountedOnce(t *testing.T) {
	dir := blockerLedgerTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", dir)
	e := event{SessionID: "session", TurnID: "failed", CWD: blockerLedgerTestDir(t)}
	if _, err := ledgerRecord(e, "deploy", "deploy", blockerStatusFailed); err != nil {
		t.Fatal(err)
	}
	ledger, err := ledgerRecord(event{SessionID: e.SessionID, TurnID: "recovered", CWD: e.CWD}, "deploy", "deploy", blockerStatusSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.snapshot(); got.Unresolved != 0 || got.Recoveries != 1 || got.LastRecoveryTurn != "recovered" {
		t.Fatalf("matched recovery = %#v", got)
	}
	ledger, err = ledgerRecord(event{SessionID: e.SessionID, TurnID: "again", CWD: e.CWD}, "deploy", "deploy", blockerStatusSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.snapshot(); got.Recoveries != 1 {
		t.Fatalf("duplicate success earned a second recovery: %#v", got)
	}
}

func TestBlockerLedgerPersistsAcrossTurnsAndScopesWorkingDirectory(t *testing.T) {
	dir := blockerLedgerTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", dir)
	firstRepo, secondRepo := blockerLedgerTestDir(t), blockerLedgerTestDir(t)
	if _, err := ledgerRecord(event{SessionID: "session", TurnID: "one", CWD: firstRepo}, "deploy", "deploy", blockerStatusFailed); err != nil {
		t.Fatal(err)
	}
	continued, err := ledgerLoad(event{SessionID: "session", TurnID: "two", CWD: firstRepo})
	if err != nil || continued.snapshot().Unresolved != 1 {
		t.Fatalf("ledger did not persist across turns: %#v err=%v", continued, err)
	}
	other, err := ledgerLoad(event{SessionID: "session", TurnID: "two", CWD: secondRepo})
	if err != nil || other.snapshot().Unresolved != 0 {
		t.Fatalf("other working directory inherited ledger: %#v err=%v", other, err)
	}
}

func TestBlockerLedgerClaimedSuccessDoesNotEarnRecovery(t *testing.T) {
	dir := blockerLedgerTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", dir)
	e := event{SessionID: "session", TurnID: "claimed", CWD: blockerLedgerTestDir(t)}
	if _, err := ledgerRecord(e, "ship", "ship", blockerStatusClaimed); err != nil {
		t.Fatal(err)
	}
	claimed, err := ledgerLoad(e)
	if err != nil || claimed.snapshot().Unsupported != 1 {
		t.Fatalf("evidence-free claim was not unsupported: %#v err=%v", claimed, err)
	}
	ledger, err := ledgerRecord(event{SessionID: e.SessionID, TurnID: "success", CWD: e.CWD}, "ship", "ship", blockerStatusSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.snapshot(); got.Unresolved != 0 || got.Recoveries != 0 {
		t.Fatalf("claim resolution earned recovery: %#v", got)
	}
}

func TestBlockerLedgerNativeDeployRecoversNativeShipOnly(t *testing.T) {
	dir := blockerLedgerTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", dir)
	e := event{SessionID: "session", TurnID: "blocked", CWD: blockerLedgerTestDir(t)}
	for _, record := range []struct {
		key, kind, status string
	}{
		{"deploy", "deploy", blockerStatusClaimed},
		{"ship", "ship", blockerStatusClaimed},
		{"ship", "ship", blockerStatusFailed},
		{"other", "ship", blockerStatusFailed},
	} {
		if _, err := ledgerRecord(e, record.key, record.kind, record.status); err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := ledgerRecord(event{SessionID: e.SessionID, TurnID: "deployed", CWD: e.CWD}, "deploy", "deploy", blockerStatusSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.snapshot(); got.Unresolved != 1 || got.Recoveries != 1 || got.LastRecoveryTurn != "deployed" {
		t.Fatalf("native deploy recovery = %#v", got)
	}
	if ledger.Obligations[0].OperationKey != "other" {
		t.Fatalf("native deploy cleared a non-native obligation: %#v", ledger.Obligations)
	}
}

func TestBlockerLedgerChallengeOnlyOncePerEpisode(t *testing.T) {
	dir := blockerLedgerTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", dir)
	e := event{SessionID: "session", TurnID: "failed", CWD: blockerLedgerTestDir(t)}
	if _, err := ledgerRecord(e, "deploy", "deploy", blockerStatusFailed); err != nil {
		t.Fatal(err)
	}
	_, challenge, err := ledgerChallenge(event{SessionID: e.SessionID, TurnID: "stop", CWD: e.CWD})
	if err != nil || !challenge {
		t.Fatalf("first challenge = %v, err=%v", challenge, err)
	}
	_, challenge, err = ledgerChallenge(event{SessionID: e.SessionID, TurnID: "stop-again", CWD: e.CWD})
	if err != nil || challenge {
		t.Fatalf("repeated challenge = %v, err=%v", challenge, err)
	}
	if _, err := ledgerRecord(event{SessionID: e.SessionID, TurnID: "ok", CWD: e.CWD}, "deploy", "deploy", blockerStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if _, err := ledgerRecord(event{SessionID: e.SessionID, TurnID: "failed-again", CWD: e.CWD}, "deploy", "deploy", blockerStatusFailed); err != nil {
		t.Fatal(err)
	}
	_, challenge, err = ledgerChallenge(event{SessionID: e.SessionID, TurnID: "new-stop", CWD: e.CWD})
	if err != nil || !challenge {
		t.Fatalf("new episode challenge = %v, err=%v", challenge, err)
	}
}

func TestBlockerLedgerUsesCanonicalCWDAndPrivateFile(t *testing.T) {
	dir := blockerLedgerTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", dir)
	repo := blockerLedgerTestDir(t)
	link := filepath.Join(blockerLedgerTestDir(t), "repo-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	e := event{SessionID: "session", TurnID: "one", CWD: link}
	if _, err := ledgerRecord(e, "deploy", "deploy", blockerStatusUnknown); err != nil {
		t.Fatal(err)
	}
	ledger, err := ledgerLoad(event{SessionID: "session", TurnID: "two", CWD: repo})
	if err != nil || ledger.snapshot().Unsupported != 1 {
		t.Fatalf("symlink was not canonicalized: %#v err=%v", ledger, err)
	}
	path, _, err := blockerLedgerPath(e)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger permissions = %v err=%v", info.Mode().Perm(), err)
	}
}
