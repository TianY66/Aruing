package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Aruing/Aruing/internal/core"
)

func TestNamespaceAmbiguityIgnoresAnswersBeforeLookup(t *testing.T) {
	query := core.Query{Nodes: []core.Node{{
		ID:   "n_demo_api",
		Text: "demo-api",
		Attrs: map[string]string{
			"k8s.name": "demo-api",
		},
	}}}
	raw, err := json.Marshal(map[string]any{
		"exitCode": 0,
		"stdout": "NAMESPACE  NAME\n" +
			"team-a     deployment.apps/demo-api\n" +
			"team-b     deployment.apps/demo-api\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ResolveState{
		Tasks: []core.Task{{
			ID:        "t_list",
			ToolName:  "k8s",
			Arguments: json.RawMessage(`{"argv":["get","deployments","-A"]}`),
		}},
		Evidence: []core.Evidence{{
			ID:       "e_list",
			TaskID:   "t_list",
			ToolName: "k8s",
			Raw:      raw,
		}},
		Clarifications: []string{"team-a was mentioned in an earlier, unrelated answer"},
	}
	action := ResolveAction{
		Action: ResolveActionSubmitTargets,
		Targets: []ProposedTarget{{
			NodeID: "n_demo_api",
			Attrs:  map[string]string{"k8s.name": "demo-api", "k8s.namespace": "team-b"},
		}},
	}

	clarify := namespaceAmbiguity(query, state, action)
	if clarify == nil || !strings.Contains(clarify.Question, "team-a") || !strings.Contains(clarify.Question, "team-b") {
		t.Fatalf("clarification = %+v, want both candidates despite earlier answer", clarify)
	}
}

func TestNamespaceAmbiguityDoesNotCombineDifferentKinds(t *testing.T) {
	query := core.Query{Nodes: []core.Node{{
		ID:   "n_demo_api",
		Text: "demo-api",
		Attrs: map[string]string{
			"k8s.name": "demo-api",
		},
	}}}
	objects := []namespacedObject{
		{Kind: "deployment", Name: "demo-api", Namespace: "team-a"},
		{Kind: "service", Name: "demo-api", Namespace: "team-b"},
	}
	if namespaces := matchingNamespaces(objects, "demo-api", ""); len(namespaces) != 0 {
		t.Fatalf("different resource kinds were combined into candidates %v", namespaces)
	}
	action := ResolveAction{
		Action: ResolveActionSubmitTargets,
		Targets: []ProposedTarget{{
			NodeID: "n_demo_api",
			Attrs:  map[string]string{"k8s.name": "deployment.apps/demo-api"},
		}},
	}
	state := ResolveState{
		Tasks: []core.Task{{
			ID:        "t_list",
			ToolName:  "k8s",
			Arguments: json.RawMessage(`{"argv":["get","deployments,services","-A"]}`),
		}},
		Evidence: []core.Evidence{{
			ID:       "e_list",
			TaskID:   "t_list",
			ToolName: "k8s",
			Raw: marshalNamespaceTestJSON(t, map[string]any{
				"exitCode": 0,
				"stdout": "NAMESPACE  NAME\n" +
					"team-a     deployment.apps/demo-api\n" +
					"team-b     service/demo-api\n",
			}),
		}},
	}
	if clarify := namespaceAmbiguity(query, state, action); clarify != nil {
		t.Fatalf("unexpected namespace ambiguity across different kinds: %+v", clarify)
	}
}

func TestNamespaceLookupUsesWorkloadNameForGeneratedPod(t *testing.T) {
	node := &core.Node{ID: "n_bad_label", Text: "1）"}
	got := namespaceLookupName(node, map[string]string{
		"k8s.kind": "Pod",
		"k8s.name": "demo-api-7c467667c-qw7kt",
	}, "k8s.resource")
	if got != "demo-api" {
		t.Fatalf("namespace lookup name = %q, want demo-api", got)
	}
}

func TestNamespaceAmbiguityKeepsKindSpecificCandidatesSeparate(t *testing.T) {
	query := core.Query{Nodes: []core.Node{{
		ID:   "n_demo_api",
		Text: "demo-api",
	}}}
	state := ResolveState{
		Tasks: []core.Task{{
			ID:        "t_list",
			ToolName:  "k8s",
			Arguments: json.RawMessage(`{"argv":["get","deployments,services","-A"]}`),
		}},
		Evidence: []core.Evidence{{
			ID:       "e_list",
			TaskID:   "t_list",
			ToolName: "k8s",
			Raw: marshalNamespaceTestJSON(t, map[string]any{
				"exitCode": 0,
				"stdout": "NAMESPACE  NAME\n" +
					"team-a     deployment.apps/demo-api\n" +
					"team-b     deployment.apps/demo-api\n" +
					"team-c     service/demo-api\n" +
					"team-d     service/demo-api\n",
			}),
		}},
	}
	clarify := namespaceAmbiguity(query, state, ResolveAction{
		Action:  ResolveActionSubmitTargets,
		Targets: []ProposedTarget{{NodeID: "n_demo_api"}},
	})
	if clarify == nil {
		t.Fatal("expected resource kind clarification")
	}
	if clarify.TargetNodeID != "n_demo_api" || clarify.TargetName != "demo-api" {
		t.Fatalf("clarification target = %q/%q, want n_demo_api/demo-api", clarify.TargetNodeID, clarify.TargetName)
	}
	want := []string{"deployment/team-a", "deployment/team-b", "service/team-c", "service/team-d"}
	if strings.Join(clarify.Options, ",") != strings.Join(want, ",") {
		t.Fatalf("options = %v, want kind-scoped candidates %v", clarify.Options, want)
	}
}

func TestNamespaceAmbiguityKeepsCustomResourceAPIGroupsSeparate(t *testing.T) {
	query := core.Query{Nodes: []core.Node{{
		ID:   "n_widget",
		Text: "widget",
		Attrs: map[string]string{
			"k8s.name": "widget",
		},
	}}}
	state := ResolveState{
		Tasks: []core.Task{{
			ID: "t_list", ToolName: "k8s",
			Arguments: json.RawMessage(`{"argv":["get","widgets.alpha.example.com,widgets.beta.example.com","-A"]}`),
		}},
		Evidence: []core.Evidence{{
			ID: "e_list", TaskID: "t_list", ToolName: "k8s",
			Raw: marshalNamespaceTestJSON(t, map[string]any{
				"exitCode": 0,
				"stdout": "NAMESPACE  NAME\n" +
					"team-a     widgets.alpha.example.com/widget\n" +
					"team-b     widgets.alpha.example.com/widget\n" +
					"team-c     widgets.beta.example.com/widget\n" +
					"team-d     widgets.beta.example.com/widget\n",
			}),
		}},
	}
	clarify := namespaceAmbiguity(query, state, ResolveAction{
		Action:  ResolveActionSubmitTargets,
		Targets: []ProposedTarget{{NodeID: "n_widget", Attrs: map[string]string{"k8s.name": "widget"}}},
	})
	if clarify == nil {
		t.Fatal("expected API-group clarification for same-Kind custom resources")
	}
	want := []string{
		"widget.alpha.example.com/team-a", "widget.alpha.example.com/team-b",
		"widget.beta.example.com/team-c", "widget.beta.example.com/team-d",
	}
	if strings.Join(clarify.Options, ",") != strings.Join(want, ",") {
		t.Fatalf("options = %v, want API-group-qualified candidates %v", clarify.Options, want)
	}
}

func TestNamespaceCandidateSelectionRetainsCoreAPIGroup(t *testing.T) {
	objects := []namespacedObject{
		{Kind: "event", Name: "event", Namespace: "team-a"},
		{Kind: "event", Name: "event", Namespace: "team-b"},
		{Kind: "event", APIGroup: "events.k8s.io", Name: "event", Namespace: "team-c"},
		{Kind: "event", APIGroup: "events.k8s.io", Name: "event", Namespace: "team-d"},
	}
	options := namespaceCandidateOptions(duplicateNamespaceGroups(objects, "event", "event"))
	want := []string{
		"event.@core/team-a", "event.@core/team-b",
		"event.events.k8s.io/team-c", "event.events.k8s.io/team-d",
	}
	if strings.Join(options, ",") != strings.Join(want, ",") {
		t.Fatalf("options = %v, want core-group-qualified candidates %v", options, want)
	}
	coreGroups := duplicateNamespaceGroupsByAPIGroup(objects, "event", "event", "@core")
	if len(coreGroups) != 1 || strings.Join(coreGroups["event"], ",") != "team-a,team-b" {
		t.Fatalf("core API-group filter = %v, want only team-a/team-b", coreGroups)
	}

	query := core.Query{Nodes: []core.Node{{ID: "n_event", Text: "event", Attrs: map[string]string{"k8s.name": "event"}}}}
	state := ResolveState{
		NamespaceSelections: []NamespaceSelection{{
			NodeID: "n_event", Name: "event", Kind: "event", APIGroup: "@core", Namespace: "team-b",
		}},
		Tasks: []core.Task{{ID: "t_list", ToolName: "k8s", Arguments: json.RawMessage(`{"argv":["get","events,events.events.k8s.io","-A"]}`)}},
		Evidence: []core.Evidence{{
			ID: "e_list", TaskID: "t_list", ToolName: "k8s",
			Raw: marshalNamespaceTestJSON(t, map[string]any{
				"exitCode": 0,
				"stdout": "NAMESPACE  NAME\n" +
					"team-a     events/event\n" +
					"team-b     events/event\n" +
					"team-c     events.events.k8s.io/event\n" +
					"team-d     events.events.k8s.io/event\n",
			}),
		}},
	}
	action := ResolveAction{Action: ResolveActionSubmitTargets, Targets: []ProposedTarget{{
		NodeID: "n_event", Type: "k8s.resource", Attrs: map[string]string{"k8s.kind": "Event", "k8s.name": "event"},
	}}}
	if clarify := namespaceAmbiguity(query, state, action); clarify != nil {
		t.Fatalf("core group selection repeated clarification: %+v", clarify)
	}
	if got := action.Targets[0].Attrs["k8s.namespace"]; got != "team-b" {
		t.Fatalf("core group namespace = %q, want team-b", got)
	}

	singletonGroupObjects := append(slices.Clone(objects[:2]), namespacedObject{
		Kind: "event", APIGroup: "events.k8s.io", Name: "event", Namespace: "team-c",
	})
	singletonGroups := duplicateNamespaceGroups(singletonGroupObjects, "event", "event")
	wantSingletonOptions := []string{
		"event.@core/team-a", "event.@core/team-b", "event.events.k8s.io/team-c",
	}
	if got := namespaceCandidateOptions(singletonGroups); strings.Join(got, ",") != strings.Join(wantSingletonOptions, ",") {
		t.Fatalf("singleton API-group options = %v, want %v", got, wantSingletonOptions)
	}
	singletonState := state
	singletonState.Evidence = []core.Evidence{{
		ID: "e_list", TaskID: "t_list", ToolName: "k8s",
		Raw: marshalNamespaceTestJSON(t, map[string]any{
			"exitCode": 0,
			"stdout": "NAMESPACE  NAME\n" +
				"team-a     events/event\n" +
				"team-b     events/event\n" +
				"team-c     events.events.k8s.io/event\n",
		}),
	}}
	singletonAction := ResolveAction{Action: ResolveActionSubmitTargets, Targets: []ProposedTarget{{
		NodeID: "n_event", Type: "k8s.resource",
		Attrs: map[string]string{"k8s.kind": "Event", "k8s.apiGroup": "events.k8s.io", "k8s.name": "event"},
	}}}
	if clarify := namespaceAmbiguity(query, singletonState, singletonAction); clarify != nil {
		t.Fatalf("core selection did not bind against a singleton named API group: %+v", clarify)
	}
	if got := singletonAction.Targets[0].Attrs["k8s.apiGroup"]; got != "" {
		t.Fatalf("API group after core selection = %q, want core group", got)
	}
	if got := singletonAction.Targets[0].Attrs["k8s.namespace"]; got != "team-b" {
		t.Fatalf("namespace after core selection = %q, want team-b", got)
	}

	singletonState.NamespaceSelections = []NamespaceSelection{{
		NodeID: "n_event", Name: "event", Kind: "event", APIGroup: "events.k8s.io", Namespace: "team-c",
	}}
	conflictingAction := ResolveAction{Action: ResolveActionSubmitTargets, Targets: []ProposedTarget{{
		NodeID: "n_event", Type: "k8s.resource",
		Attrs: map[string]string{"k8s.kind": "Event", "k8s.name": "event"},
	}}}
	if clarify := namespaceAmbiguity(query, singletonState, conflictingAction); clarify != nil {
		t.Fatalf("explicit singleton group selection was not applied: %+v", clarify)
	}
	if got := conflictingAction.Targets[0].Attrs["k8s.namespace"]; got != "team-c" {
		t.Fatalf("singleton group namespace = %q, want team-c", got)
	}
	if got := conflictingAction.Targets[0].Attrs["k8s.apiGroup"]; got != "events.k8s.io" {
		t.Fatalf("singleton group API group = %q, want events.k8s.io", got)
	}
}

func TestNamespaceSelectionDoesNotCrossCustomResourceAPIGroups(t *testing.T) {
	query := core.Query{Nodes: []core.Node{{ID: "n_widget", Text: "widget", Attrs: map[string]string{"k8s.name": "widget"}}}}
	state := ResolveState{
		NamespaceSelections: []NamespaceSelection{{
			NodeID: "n_widget", Name: "widget", Kind: "widget", APIGroup: "alpha.example.com", Namespace: "team-b",
		}},
		Tasks: []core.Task{{
			ID: "t_list", ToolName: "k8s",
			Arguments: json.RawMessage(`{"argv":["get","widgets.alpha.example.com,widgets.beta.example.com","-A"]}`),
		}},
		Evidence: []core.Evidence{{
			ID: "e_list", TaskID: "t_list", ToolName: "k8s",
			Raw: marshalNamespaceTestJSON(t, map[string]any{
				"exitCode": 0,
				"stdout": "NAMESPACE  NAME\n" +
					"team-a     widgets.alpha.example.com/widget\n" +
					"team-b     widgets.alpha.example.com/widget\n" +
					"team-c     widgets.beta.example.com/widget\n" +
					"team-d     widgets.beta.example.com/widget\n",
			}),
		}},
	}
	action := ResolveAction{Action: ResolveActionSubmitTargets, Targets: []ProposedTarget{
		{NodeID: "n_widget", Type: "k8s.resource", Attrs: map[string]string{"k8s.kind": "Widget", "k8s.apiGroup": "alpha.example.com", "k8s.name": "widget"}},
		{NodeID: "n_widget", Type: "k8s.resource", Attrs: map[string]string{"k8s.kind": "Widget", "k8s.apiGroup": "beta.example.com", "k8s.name": "widget"}},
	}}
	clarify := namespaceAmbiguity(query, state, action)
	if clarify == nil || clarify.TargetAPIGroup != "beta.example.com" {
		t.Fatalf("clarification = %+v, want separate beta.example.com selection", clarify)
	}
	if got := action.Targets[0].Attrs["k8s.namespace"]; got != "team-b" {
		t.Fatalf("alpha group namespace = %q, want selected team-b", got)
	}
	if got := action.Targets[1].Attrs["k8s.namespace"]; got != "" {
		t.Fatalf("beta group namespace = %q, must not inherit alpha selection", got)
	}
}

func TestParseNamespacedObjectsPreservesAPIGroups(t *testing.T) {
	objects := parseNamespacedObjects("NAMESPACE  NAME\nteam-a     widgets.alpha.example.com/widget\n", "")
	if len(objects) != 1 || objects[0].Kind != "widget" || objects[0].APIGroup != "alpha.example.com" {
		t.Fatalf("parsed table objects = %+v, want Widget in alpha.example.com", objects)
	}
	objects = parseNamespacedObjects(`{"apiVersion":"beta.example.com/v1","items":[{"kind":"Widget","metadata":{"name":"widget","namespace":"team-b"}}]}`, "")
	if len(objects) != 1 || objects[0].Kind != "widget" || objects[0].APIGroup != "beta.example.com" {
		t.Fatalf("parsed JSON objects = %+v, want Widget in beta.example.com", objects)
	}
}

func TestNamespaceSelectionOnlyAppliesToItsTarget(t *testing.T) {
	query := core.Query{Nodes: []core.Node{
		{ID: "n_demo_api", Text: "demo-api", Attrs: map[string]string{"k8s.kind": "Deployment", "k8s.name": "demo-api"}},
		{ID: "n_worker", Text: "worker", Attrs: map[string]string{"k8s.kind": "Deployment", "k8s.name": "worker"}},
	}}
	state := ResolveState{
		NamespaceSelection:     "team-b",
		NamespaceSelectionKind: "deployment",
		NamespaceSelections: []NamespaceSelection{{
			NodeID: "n_demo_api", Name: "demo-api", Kind: "deployment", Namespace: "team-b",
		}},
		Tasks: []core.Task{{
			ID: "t_list", ToolName: "k8s", Arguments: json.RawMessage(`{"argv":["get","deployments","-A"]}`),
		}},
		Evidence: []core.Evidence{{
			ID: "e_list", TaskID: "t_list", ToolName: "k8s",
			Raw: marshalNamespaceTestJSON(t, map[string]any{
				"exitCode": 0,
				"stdout": "NAMESPACE  NAME\n" +
					"team-a     deployment.apps/demo-api\n" +
					"team-b     deployment.apps/demo-api\n" +
					"team-a     deployment.apps/worker\n" +
					"team-b     deployment.apps/worker\n",
			}),
		}},
	}
	action := ResolveAction{Action: ResolveActionSubmitTargets, Targets: []ProposedTarget{
		{NodeID: "n_demo_api", Type: "k8s.resource", Attrs: map[string]string{"k8s.kind": "Deployment", "k8s.name": "demo-api"}},
		{NodeID: "n_worker", Type: "k8s.resource", Attrs: map[string]string{"k8s.kind": "Deployment", "k8s.name": "worker"}},
	}}

	clarify := namespaceAmbiguity(query, state, action)
	if clarify == nil {
		t.Fatal("expected second target to require its own namespace selection")
	}
	if clarify.TargetNodeID != "n_worker" || clarify.TargetName != "worker" {
		t.Fatalf("clarification target = %q/%q, want n_worker/worker", clarify.TargetNodeID, clarify.TargetName)
	}
	if strings.Join(clarify.Options, ",") != "team-a,team-b" {
		t.Fatalf("options = %v, want only the second target's candidates", clarify.Options)
	}
}

func TestNamespaceSelectionDoesNotCrossResourceKindsOnSameNode(t *testing.T) {
	query := core.Query{Nodes: []core.Node{{ID: "n_demo_api", Text: "demo-api", Attrs: map[string]string{"k8s.name": "demo-api"}}}}
	state := ResolveState{
		NamespaceSelections: []NamespaceSelection{{
			NodeID: "n_demo_api", Name: "demo-api", Kind: "deployment", Namespace: "team-b",
		}},
		Tasks: []core.Task{{
			ID: "t_list", ToolName: "k8s", Arguments: json.RawMessage(`{"argv":["get","deployments,services","-A"]}`),
		}},
		Evidence: []core.Evidence{{
			ID: "e_list", TaskID: "t_list", ToolName: "k8s",
			Raw: marshalNamespaceTestJSON(t, map[string]any{
				"exitCode": 0,
				"stdout": "NAMESPACE  NAME\n" +
					"team-a     deployment.apps/demo-api\n" +
					"team-b     deployment.apps/demo-api\n" +
					"team-a     service/demo-api\n" +
					"team-b     service/demo-api\n",
			}),
		}},
	}
	action := ResolveAction{Action: ResolveActionSubmitTargets, Targets: []ProposedTarget{
		{NodeID: "n_demo_api", Type: "k8s.resource", Attrs: map[string]string{"k8s.kind": "Deployment", "k8s.name": "demo-api"}},
		{NodeID: "n_demo_api", Type: "k8s.resource", Attrs: map[string]string{"k8s.kind": "Service", "k8s.name": "demo-api"}},
	}}

	clarify := namespaceAmbiguity(query, state, action)
	if clarify == nil || clarify.TargetKind != "service" {
		t.Fatalf("clarification = %+v, want a separate Service choice", clarify)
	}
	if got := action.Targets[1].Attrs["k8s.namespace"]; got != "" {
		t.Fatalf("Service namespace = %q, want no namespace inherited from Deployment choice", got)
	}
	if got := action.Targets[1].Attrs["k8s.kind"]; got != "Service" {
		t.Fatalf("Service kind = %q, want unchanged Service", got)
	}
}

func TestNamespaceResourceInventoryIncludesCustomResources(t *testing.T) {
	stdout := "NAME      SHORTNAMES   APIVERSION       NAMESPACED   KIND\n" +
		"pods      po          v1               true         Pod\n" +
		"widgets   wd          example.com/v1   true         Widget\n" +
		"nodes     no          v1               false        Node\n"
	resources, err := namespaceResourceNames(stdout, "widget")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(resources, ",") != "pods,widgets.example.com" {
		t.Fatalf("resources = %v, want core and CRD resource identifiers", resources)
	}
	if got := namespaceScanResource(""); got != "" {
		t.Fatalf("unknown kind scan resource = %q, want dynamic API discovery", got)
	}

	staticList := []string{"get", "deployments,services,pods", "-A"}
	if hasCompleteNamespaceInventoryLookup(ResolveState{}, staticList) {
		t.Fatal("static built-in resource list must not be treated as a complete namespaced inventory")
	}
	apiResourcesArgs := json.RawMessage(`{"argv":["api-resources","--namespaced=true","--verbs=list"]}`)
	getInventoryArgs := json.RawMessage(`{"argv":["get","pods,widgets.example.com","--all-namespaces"]}`)
	state := ResolveState{
		Tasks: []core.Task{
			{ID: "t_api", ToolName: "k8s", Arguments: apiResourcesArgs},
			{ID: "t_get", ToolName: "k8s", Arguments: getInventoryArgs},
		},
		Evidence: []core.Evidence{{
			TaskID: "t_api", ToolName: "k8s",
			Raw: marshalNamespaceTestJSON(t, map[string]any{"exitCode": 0, "stdout": stdout}),
		}},
	}
	var getArgs namespaceListArgs
	if err := json.Unmarshal(getInventoryArgs, &getArgs); err != nil {
		t.Fatal(err)
	}
	if !hasCompleteNamespaceInventoryLookup(state, getArgs.Argv) {
		t.Fatal("full api-resources inventory should validate matching all-namespaces list")
	}
}

func TestNamespaceResourceBatchesSeparateSameKindsAcrossAPIGroups(t *testing.T) {
	stdout := "NAME      SHORTNAMES   APIVERSION       NAMESPACED   KIND\n" +
		"pods      po          v1               true         Pod\n" +
		"widgets   wd          alpha.example.com/v1   true   Widget\n" +
		"widgets   wd          beta.example.com/v1    true   Widget\n" +
		"people    p           alpha.example.com/v1   true   Person\n" +
		"events    ev          v1               true         Event\n" +
		"events    ev          events.k8s.io/v1  true         Event\n"
	batches, err := namespaceResourceBatches(stdout, "widget")
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %v, want two batches for duplicated Kinds", batches)
	}
	foundPersonResource := false
	for _, batch := range batches {
		seenKinds := make(map[string]struct{})
		for _, resource := range batch {
			kind := normalizeKind(resource.Kind)
			if _, exists := seenKinds[kind]; exists {
				t.Fatalf("batch %v contains duplicate Kind %q", batch, kind)
			}
			seenKinds[kind] = struct{}{}
			if resource.Name == "people.alpha.example.com" && kind == "person" {
				foundPersonResource = true
			}
		}
	}
	if !foundPersonResource {
		t.Fatal("resource with plural people and Kind Person lost its real Kind")
	}
}

func TestBroadNamespaceInventoryRejectsFilteredQueries(t *testing.T) {
	if !isBroadNamespaceResourceList([]string{"get", "pods,services", "--all-namespaces"}) {
		t.Fatal("unfiltered all-namespaces list was rejected")
	}
	for _, argv := range [][]string{
		{"get", "pods,services", "--all-namespaces", "--selector=app=demo"},
		{"get", "pods,services", "--all-namespaces", "--limit=1"},
		{"get", "pods,services", "--all-namespaces", "-o=name"},
	} {
		if isBroadNamespaceResourceList(argv) {
			t.Fatalf("filtered query %v was incorrectly accepted as complete", argv)
		}
	}
	if isNamespacedListResourceInventory([]string{"api-resources", "--namespaced=true", "--verbs=list", "--api-group=apps"}) {
		t.Fatal("API-group-filtered resource inventory was incorrectly accepted as complete")
	}
}

func marshalNamespaceTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
