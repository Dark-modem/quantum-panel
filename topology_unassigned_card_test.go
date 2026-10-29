package main

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
		for _, id := range []string{"topologyUnassignedCard", "topologyUnassigned"} {
			if strings.Contains(string(data), id) {
				t.Errorf("%s still contains removed overview element %s", name, id)
			}
		}
	}

	html := sources["index.html"]
	if !regexp.MustCompile(`<select\b[^>]*\bid="topologyAttachSelect"`).MatchString(html) {
		t.Error("existing-tunnel attachment dropdown must remain")
	}
	for _, id := range []string{
		"topologyListCard", "topologyDetailCard", "topologyGraph", "topologiesList",
		"topologyCreateBtn", "topologyBackBtn", "topologySaveBtn",
		"topologySettings", "topologyAttachBtn", "topologyBoundList", "topologyAddTunnelBtn",
	} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("removing the overview card must preserve %s", id)
		}
	}

	js := sources["topology.js"]
	for _, fragment := range []string{
		"$('topologyAttachSelect').innerHTML", "$('topologyAttachSelect').value",
		"$('topologyGraph').innerHTML", "data-topo-open", "data-topo-delete",
		"data-topo-detach", "data-topo-view-tunnel",
	} {
		if !strings.Contains(js, fragment) {
			t.Errorf("removing the overview card must preserve %s", fragment)
		}
	}
}
