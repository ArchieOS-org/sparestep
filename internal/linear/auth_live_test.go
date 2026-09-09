package linear

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in read-only check of saved OAuth and configured destinations. It uses
// a temporary empty outbox and never calls an issue creation/update tool.
func TestLiveSavedConnection(t *testing.T) {
	state := os.Getenv("SPARESTEP_LIVE_STATE")
	if state == "" {
		t.Skip("set SPARESTEP_LIVE_STATE to check an already authorized connection")
	}
	data, err := os.ReadFile(filepath.Join(state, "linear-projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	var destinations map[string]Destination
	if err = json.Unmarshal(data, &destinations); err != nil {
		t.Fatal(err)
	}
	if len(destinations) == 0 {
		t.Fatal("no configured destinations")
	}
	o, err := Open(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err = o.Connect(ctx, state, ConnectOptions{NoBrowser: true}); err != nil {
		t.Fatal("saved authorization reconnect failed:", err)
	}
	if err = o.discover(ctx); err != nil {
		t.Fatal(err)
	}
	for repository, d := range destinations {
		if err = o.Configure(d); err != nil {
			t.Fatal(err)
		}
		if err = o.validateDispatchPolicy(ctx, ""); err != nil {
			t.Fatalf("destination %s: %v", repository, err)
		}
		t.Logf("Verified saved authorization and destination %s / %s / %s", repository, d.ProjectID, d.State)
	}
}
