// models3_test.go — hermetic tests for the R5 engine artifact store:
// the duplicate-slot rule (same hash = update, different hash = conflict),
// the 1:1 artifact-name namespace, delete semantics, and the enabled
// engine counts the Engines tab warns with.

package store

import (
	"errors"
	"testing"
	"time"
)

func openEngineStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestEngineArtifactUpsertDuplicateRules(t *testing.T) {
	s := openEngineStore(t)
	row := EngineArtifact{
		Engine: "frp", Version: "0.61.1", Goos: "linux", Goarch: "amd64",
		Artifact: "frp-0.61.1-linux-amd64.tar.gz", SHA256: "aa", Size: 10,
		Signature: "sig", Uploader: "op", UploadedAt: time.Now().Unix(),
	}
	id, err := s.UpsertEngineArtifact(row)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Same slot, same hash → accepted update, same row id.
	row.Size = 11
	row.Artifact = "renamed.tar.gz"
	id2, err := s.UpsertEngineArtifact(row)
	if err != nil {
		t.Fatalf("same-hash re-upload must be accepted: %v", err)
	}
	if id2 != id {
		t.Fatalf("same-hash re-upload must update in place: got id %d want %d", id2, id)
	}
	got, err := s.GetEngineArtifact(id)
	if err != nil || got.Size != 11 || got.Artifact != "renamed.tar.gz" {
		t.Fatalf("update not applied: %+v err=%v", got, err)
	}

	// Same slot, DIFFERENT hash → ErrArtifactHashConflict.
	row.SHA256 = "bb"
	if _, err := s.UpsertEngineArtifact(row); !errors.Is(err, ErrArtifactHashConflict) {
		t.Fatalf("different hash must conflict: got %v", err)
	}

	// Artifact name owned by another slot → rejected.
	other := row
	other.Version = "0.62.0"
	other.SHA256 = "cc"
	other.Artifact = "renamed.tar.gz" // taken by the first slot
	if _, err := s.UpsertEngineArtifact(other); err == nil || err.Error() != "store: artifact download name already in use" {
		t.Fatalf("name collision must be rejected: got %v", err)
	}
	// Free name on a new slot → fine.
	other.Artifact = "frp-0.62.0-linux-amd64.tar.gz"
	if _, err := s.UpsertEngineArtifact(other); err != nil {
		t.Fatalf("new slot with free name: %v", err)
	}
}

func TestEngineArtifactListDelete(t *testing.T) {
	s := openEngineStore(t)
	for _, arch := range []string{"amd64", "arm64"} {
		_, err := s.UpsertEngineArtifact(EngineArtifact{
			Engine: "frp", Version: "0.61.1", Goos: "linux", Goarch: arch,
			Artifact: "frp-0.61.1-linux-" + arch + ".tar.gz", SHA256: "hash-" + arch,
			Size: 5, UploadedAt: time.Now().Unix(),
		})
		if err != nil {
			t.Fatalf("insert %s: %v", arch, err)
		}
	}
	arts, err := s.ListEngineArtifacts()
	if err != nil || len(arts) != 2 {
		t.Fatalf("list: %d arts err=%v", len(arts), err)
	}
	byName, err := s.GetEngineArtifactByName("frp-0.61.1-linux-arm64.tar.gz")
	if err != nil || byName.Goarch != "arm64" {
		t.Fatalf("by name: %+v err=%v", byName, err)
	}
	if _, err := s.GetEngineArtifactByName("nope.tar.gz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown name must be ErrNotFound: %v", err)
	}
	if err := s.DeleteEngineArtifact(byName.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteEngineArtifact(byName.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete must be ErrNotFound: %v", err)
	}
}

func TestEngineEnabledCounts(t *testing.T) {
	s := openEngineStore(t)
	// Topology + 2 servers so tunnels have real parents.
	dc, err := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "dc1"})
	if err != nil {
		t.Fatalf("dc: %v", err)
	}
	sv1, err := s.UpsertServer(Server{Name: "iran1", Role: "iran", DatacenterID: dc})
	if err != nil {
		t.Fatalf("srv1: %v", err)
	}
	sv2, err := s.UpsertServer(Server{Name: "fr1", Role: "foreign"})
	if err != nil {
		t.Fatalf("srv2: %v", err)
	}
	topo, err := s.UpsertTopology(Topology{Name: "t1"})
	if err != nil {
		t.Fatalf("topo: %v", err)
	}
	if _, err := s.UpsertTunnel(Tunnel{TunnelKey: "k1", TopologyID: topo, LocalServerID: sv1, RemoteServerID: sv2,
		Engine: "frp", Enabled: true}); err != nil {
		t.Fatalf("t1: %v", err)
	}
	if _, err := s.UpsertTunnel(Tunnel{TunnelKey: "k2", TopologyID: topo, LocalServerID: sv1, RemoteServerID: sv2,
		Engine: "frp", Enabled: true}); err != nil {
		t.Fatalf("t2: %v", err)
	}
	if _, err := s.UpsertTunnel(Tunnel{TunnelKey: "k3", TopologyID: topo, LocalServerID: sv1, RemoteServerID: sv2,
		Engine: "matrix", Enabled: false}); err != nil {
		t.Fatalf("t3: %v", err)
	}
	counts, err := s.EngineEnabledCounts()
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts["frp"] != 2 {
		t.Fatalf("frp count = %d, want 2 (%+v)", counts["frp"], counts)
	}
	if _, ok := counts["matrix"]; ok {
		t.Fatalf("disabled engine must not be counted: %+v", counts)
	}
}
