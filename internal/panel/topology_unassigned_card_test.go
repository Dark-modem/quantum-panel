package panel

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTopologyUnassignedCardRemoved(t *testing.T) {
	sources := make(map[string]string)
	for _, name := range []string{"index.html", "topology.js"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = string(data)
		for _, id := range []string{"topologyUnassignedCard", "topologyUnassigned", "topologyAttachSelect", "topologyAttachBtn"} {
			if strings.Contains(string(data), id) {
				t.Errorf("%s still contains removed overview element %s", name, id)
			}
		}
	}

	html := sources["index.html"]
	// the attach-existing flow (dropdown + button) was removed by owner order
	// (2026-11-18): tunnels are born into their topology and detach lives on
	// the bound list; the dropdown must NOT come back.
	if regexp.MustCompile(`<select[^>]*id="topologyAttachSelect"`).MatchString(html) {
		t.Error("removed attach dropdown must not return")
	}
	if strings.Contains(html, `id="topologyAttachBtn"`) {
		t.Error("removed attach button must not return")
	}
	for _, id := range []string{
		"topologyListCard", "topologyDetailCard", "topologyGraph", "topologiesList",
		"topologyCreateBtn", "topologyBackBtn", "topologySaveBtn",
		"topologySettings", "topologyBoundList", "topologyAddTunnelBtn",
	} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("removing the overview card must preserve %s", id)
		}
	}

	js := sources["topology.js"]
	for _, fragment := range []string{
		"$('topologyGraph').innerHTML", "data-topo-open", "data-topo-delete",
		"data-topo-detach", "data-topo-view-tunnel",
	} {
		if !strings.Contains(js, fragment) {
			t.Errorf("removing the overview card must preserve %s", fragment)
		}
	}
}
