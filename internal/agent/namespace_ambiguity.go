package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Aruing/Aruing/internal/core"
	"github.com/Aruing/Aruing/internal/session"
)

type namespaceListArgs struct {
	Argv []string `json:"argv"`
}

type namespaceListResult struct {
	ExitCode        int    `json:"exitCode"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
}

type namespacedObject struct {
	Kind      string
	APIGroup  string
	Name      string
	Namespace string
}

type namespaceResource struct {
	Name     string
	Kind     string
	APIGroup string
}

type kubernetesList struct {
	APIVersion string `json:"apiVersion"`
	Items      []struct {
		Kind       string `json:"kind"`
		APIVersion string `json:"apiVersion"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	} `json:"items"`
}

// Namespace ambiguity is checked at the orchestration boundary so a resolver
// cannot silently select one object after observing multiple namespace matches.
func namespaceAmbiguity(query core.Query, state ResolveState, action ResolveAction) *ClarifyRequest {
	if action.Action != ResolveActionSubmitTargets {
		return nil
	}
	objects := namespaceCandidates(state)
	if len(objects) == 0 {
		return nil
	}

	for i := range action.Targets {
		proposed := action.Targets[i]
		queryNode := queryNodeByID(query, proposed.NodeID)
		name := namespaceLookupName(queryNode, proposed.Attrs, proposed.Type)
		if name == "" {
			continue
		}
		kind := namespaceLookupKind(queryNode, proposed.Attrs, proposed.Type, name)
		apiGroup := namespaceLookupAPIGroup(queryNode, proposed.Attrs, proposed.Type, name)
		selection, hasSelection := namespaceSelectionForTarget(
			state, proposed.NodeID, name, kind, apiGroup,
			hasSelectedKindSibling(query, action, i, name, kind, apiGroup, state.NamespaceSelections),
			len(action.Targets),
		)
		if hasSelection && selection.Kind != "" {
			kind = selection.Kind
		}
		if hasSelection && selection.APIGroup != "" {
			apiGroup = selection.APIGroup
		}
		groups := duplicateNamespaceGroupsByAPIGroup(objects, name, kind, apiGroup)
		if len(groups) == 0 {
			if hasSelection && selection.Kind != "" && selection.Namespace != "" &&
				hasSelectedNamespaceObject(objects, name, selection.Kind, selection.APIGroup, selection.Namespace) {
				selectedKind := normalizeKind(selection.Kind)
				selectedGroup := normalizeAPIGroup(selection.APIGroup)
				if selectedGroup == "@core" {
					selectedGroup = ""
				}
				if strings.TrimSpace(proposed.Attrs["k8s.namespace"]) != selection.Namespace ||
					normalizeKind(proposed.Attrs["k8s.kind"]) != selectedKind ||
					normalizeAPIGroup(proposed.Attrs["k8s.apiGroup"]) != selectedGroup {
					attrs := make(map[string]string, len(proposed.Attrs)+3)
					for key, value := range proposed.Attrs {
						attrs[key] = value
					}
					attrs["k8s.namespace"] = selection.Namespace
					attrs["k8s.kind"] = selectedKind
					attrs["k8s.name"] = name
					if selectedGroup == "" {
						delete(attrs, "k8s.apiGroup")
					} else {
						attrs["k8s.apiGroup"] = selectedGroup
					}
					proposed.Attrs = attrs
					action.Targets[i] = proposed
				}
			}
			continue
		}
		if len(groups) > 1 {
			options := namespaceCandidateOptions(groups)
			return &ClarifyRequest{
				Question:     fmt.Sprintf("发现多个同名 Kubernetes 资源 %s，且资源类型也不唯一。请按“资源类型/namespace”选择要诊断的对象。", name),
				Options:      options,
				TargetNodeID: proposed.NodeID,
				TargetName:   name,
			}
		}
		selectedIdentity := ""
		var namespaces []string
		for selectedIdentity, namespaces = range groups {
		}
		selectedKind, selectedAPIGroup := splitNamespaceGroupIdentity(selectedIdentity)

		selected, ok := "", false
		if hasSelection && slices.Contains(namespaces, selection.Namespace) {
			selected, ok = selection.Namespace, true
		} else if !hasSelection {
			selected, ok = selectedNamespace(queryNode, "", namespaces)
		}
		if !ok {
			return &ClarifyRequest{
				Question:       fmt.Sprintf("发现多个同名 Kubernetes 资源 %s，候选 namespace：%s。请指定要诊断的 namespace。", name, strings.Join(namespaces, "、")),
				Options:        namespaces,
				TargetNodeID:   proposed.NodeID,
				TargetName:     name,
				TargetKind:     selectedKind,
				TargetAPIGroup: selectedAPIGroup,
			}
		}

		// 用户明确选择的 namespace 是目标身份的一部分。若 resolver 仍提交了另一个
		// 同名目标，以用户选择为准，避免澄清流程结束后又静默诊断错对象。
		if strings.TrimSpace(proposed.Attrs["k8s.namespace"]) != selected ||
			hasSelection && selection.Kind != "" && (normalizeKind(proposed.Attrs["k8s.kind"]) != selectedKind ||
				normalizeAPIGroup(proposed.Attrs["k8s.apiGroup"]) != selectedAPIGroup) {
			attrs := make(map[string]string, len(proposed.Attrs)+1)
			for key, value := range proposed.Attrs {
				attrs[key] = value
			}
			attrs["k8s.namespace"] = selected
			if hasSelection && selection.Kind != "" {
				attrs["k8s.kind"] = selectedKind
				attrs["k8s.name"] = name
				if selectedAPIGroup != "" {
					attrs["k8s.apiGroup"] = selectedAPIGroup
				} else {
					delete(attrs, "k8s.apiGroup")
				}
			}
			proposed.Attrs = attrs
			action.Targets[i] = proposed
		}
	}
	return nil
}

func hasSelectedNamespaceObject(objects []namespacedObject, name, kind, apiGroup, namespace string) bool {
	name = normalizeObjectName(name)
	kind = normalizeKind(kind)
	apiGroup = normalizeAPIGroup(apiGroup)
	if apiGroup == "@core" {
		apiGroup = ""
	}
	for _, object := range objects {
		if normalizeObjectName(object.Name) == name && normalizeKind(object.Kind) == kind &&
			normalizeAPIGroup(object.APIGroup) == apiGroup && object.Namespace == namespace {
			return true
		}
	}
	return false
}

func queryNodeByID(query core.Query, id string) *core.Node {
	for i := range query.Nodes {
		if query.Nodes[i].ID == id {
			return &query.Nodes[i]
		}
	}
	return nil
}

func namespaceAmbiguityForQuery(query core.Query, state ResolveState) *ClarifyRequest {
	action := ResolveAction{Action: ResolveActionSubmitTargets}
	for _, node := range query.Nodes {
		attrs := make(map[string]string, 4)
		for _, key := range []string{"k8s.kind", "k8s.apiGroup", "k8s.name", "k8s.namespace"} {
			if value := strings.TrimSpace(node.Attrs[key]); value != "" {
				attrs[key] = value
			}
		}
		action.Targets = append(action.Targets, ProposedTarget{NodeID: node.ID, Attrs: attrs})
	}
	return namespaceAmbiguity(query, state, action)
}

func rememberNamespaceClarification(state *ResolveState, clarification *ClarifyRequest) {
	if state == nil || clarification == nil {
		return
	}
	state.NamespaceSelectionOptions = slices.Clone(clarification.Options)
	state.NamespaceSelectionTargetNodeID = clarification.TargetNodeID
	state.NamespaceSelectionTargetName = clarification.TargetName
	state.NamespaceSelectionTargetKind = normalizeKind(clarification.TargetKind)
	state.NamespaceSelectionTargetAPIGroup = normalizeAPIGroup(clarification.TargetAPIGroup)
}

func namespaceSelectionForTarget(
	state ResolveState,
	nodeID string,
	name string,
	currentKind string,
	currentAPIGroup string,
	hasSelectedKindSibling bool,
	targetCount int,
) (NamespaceSelection, bool) {
	name = normalizeObjectName(name)
	var fallback NamespaceSelection
	var hasFallback bool
	for index := len(state.NamespaceSelections) - 1; index >= 0; index-- {
		selection := state.NamespaceSelections[index]
		if selection.NodeID != nodeID || normalizeObjectName(selection.Name) != name {
			continue
		}
		kindMatches := selection.Kind == "" || currentKind == "" || normalizeKind(currentKind) == normalizeKind(selection.Kind)
		groupMatches := selection.APIGroup == "" || currentAPIGroup == "" || normalizeAPIGroup(currentAPIGroup) == normalizeAPIGroup(selection.APIGroup)
		if kindMatches && groupMatches {
			if selection.APIGroup != "" && currentAPIGroup == "" && hasSelectedKindSibling {
				continue
			}
			return selection, true
		}
		if hasSelectedKindSibling {
			continue
		}
		if !hasFallback {
			fallback, hasFallback = selection, true
		}
	}
	if hasFallback {
		return fallback, true
	}
	// Legacy scalar state is only safe when the submission contains one target.
	if len(state.NamespaceSelections) == 0 && targetCount == 1 && strings.TrimSpace(state.NamespaceSelection) != "" {
		return NamespaceSelection{
			NodeID:    nodeID,
			Name:      name,
			Kind:      normalizeKind(state.NamespaceSelectionKind),
			APIGroup:  normalizeAPIGroup(state.NamespaceSelectionAPIGroup),
			Namespace: strings.TrimSpace(state.NamespaceSelection),
		}, true
	}
	return NamespaceSelection{}, false
}

func hasSelectedKindSibling(
	query core.Query,
	action ResolveAction,
	currentIndex int,
	name string,
	currentKind string,
	currentAPIGroup string,
	selections []NamespaceSelection,
) bool {
	for _, selection := range selections {
		if selection.Kind == "" {
			continue
		}
		for index, target := range action.Targets {
			if index == currentIndex || target.NodeID != action.Targets[currentIndex].NodeID {
				continue
			}
			node := queryNodeByID(query, target.NodeID)
			otherName := namespaceLookupName(node, target.Attrs, target.Type)
			if normalizeObjectName(otherName) != normalizeObjectName(name) {
				continue
			}
			otherKind := namespaceLookupKind(node, target.Attrs, target.Type, otherName)
			otherGroup := namespaceLookupAPIGroup(node, target.Attrs, target.Type, otherName)
			selectionKind := normalizeKind(selection.Kind)
			if normalizeKind(otherKind) != selectionKind {
				continue
			}
			otherMatchesSelection := selection.APIGroup == "" || otherGroup == "" ||
				normalizeAPIGroup(otherGroup) == normalizeAPIGroup(selection.APIGroup)
			currentMatchesSelection := normalizeKind(currentKind) == "" || normalizeKind(currentKind) == selectionKind
			if selection.APIGroup != "" && currentAPIGroup != "" &&
				normalizeAPIGroup(currentAPIGroup) != normalizeAPIGroup(selection.APIGroup) {
				currentMatchesSelection = false
			}
			// A kind/group-bound answer belongs to the sibling that matches it. Do not
			// let a same-name target of another kind or API group inherit that choice.
			if otherMatchesSelection && !currentMatchesSelection {
				return true
			}
			// If this target omitted its API group while a same-Kind sibling also
			// omitted it, the binding cannot safely identify which CRD it represents.
			if selection.APIGroup != "" && currentAPIGroup == "" && otherGroup == "" {
				return true
			}
		}
	}
	return false
}

// SuspendForNamespaceAmbiguity turns a namespaced duplicate found by the chat
// tool loop into the same persisted resolve-stage pause used by normal runs.
func (o *Orchestrator) SuspendForNamespaceAmbiguity(
	ctx context.Context,
	run core.Run,
	seed session.NamespaceClarificationSeed,
) (core.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return core.Outcome{}, fmt.Errorf("suspend namespace ambiguity: %w", err)
	}
	if err := o.validate(); err != nil {
		return core.Outcome{}, err
	}
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.Question) == "" {
		return core.Outcome{}, fmt.Errorf("suspend namespace ambiguity: run id and question are required")
	}
	query, err := o.parser.Parse(ctx, run)
	if err != nil {
		return core.Outcome{}, fmt.Errorf("parse namespace ambiguity query: %w", err)
	}
	query.RunID = run.ID
	state := ResolveState{
		Query:    query,
		Tasks:    slices.Clone(seed.Tasks),
		Evidence: slices.Clone(seed.Evidence),
		Round:    len(seed.Tasks),
	}
	for i := range state.Tasks {
		state.Tasks[i].RunID = run.ID
		state.Tasks[i].Arguments = slices.Clone(state.Tasks[i].Arguments)
	}
	for i := range state.Evidence {
		state.Evidence[i].RunID = run.ID
		state.Evidence[i].Raw = slices.Clone(state.Evidence[i].Raw)
	}
	clarify := namespaceAmbiguityForQuery(query, state)
	if clarify == nil {
		return core.Outcome{}, nil
	}
	rememberNamespaceClarification(&state, clarify)
	o.putResolveSuspended(run, query, *clarify, state)
	return core.Outcome{
		Suspension: &core.Suspension{
			RunID:     run.ID,
			SessionID: run.SessionID,
			Stage:     core.StageResolve,
			Question:  clarify.Question,
			Options:   slices.Clone(clarify.Options),
		},
		Evidence: slices.Clone(state.Evidence),
	}, nil
}

func namespaceCandidates(state ResolveState) []namespacedObject {
	tasks := make(map[string]core.Task, len(state.Tasks))
	for _, task := range state.Tasks {
		if task.ID != "" {
			tasks[task.ID] = task
		}
	}

	var candidates []namespacedObject
	for _, evidence := range state.Evidence {
		if evidence.ToolName != "k8s" || evidence.Error != "" {
			continue
		}
		task, ok := tasks[evidence.TaskID]
		if !ok || task.ToolName != "k8s" {
			continue
		}
		var args namespaceListArgs
		if json.Unmarshal(task.Arguments, &args) != nil || !isAllNamespaceGet(args.Argv) {
			continue
		}
		var result namespaceListResult
		if json.Unmarshal(evidence.Raw, &result) != nil || result.ExitCode != 0 || result.StdoutTruncated || !hasParsableNamespacedList(result.Stdout) {
			continue
		}
		candidates = append(candidates, parseNamespacedObjects(result.Stdout, fallbackKind(args.Argv))...)
	}
	return candidates
}

func hasCrossNamespaceDuplicateNames(raw json.RawMessage) bool {
	var result namespaceListResult
	if json.Unmarshal(raw, &result) != nil || result.ExitCode != 0 {
		return false
	}
	return hasDuplicateObjectIdentity(parseNamespacedObjects(result.Stdout, ""))
}

func hasDuplicateObjectIdentity(objects []namespacedObject) bool {
	seen := make(map[string]map[string]struct{})
	for _, object := range objects {
		if object.Name == "" || object.Namespace == "" {
			continue
		}
		identity := object.Kind + "\x00" + object.APIGroup + "\x00" + object.Name
		if seen[identity] == nil {
			seen[identity] = make(map[string]struct{})
		}
		seen[identity][object.Namespace] = struct{}{}
	}
	for _, namespaces := range seen {
		if len(namespaces) > 1 {
			return true
		}
	}
	return false
}

func isAllNamespaceGet(argv []string) bool {
	if len(argv) < 2 || !strings.EqualFold(argv[0], "get") {
		return false
	}
	for _, arg := range argv[1:] {
		switch arg {
		case "-A", "--all-namespaces", "--all-namespaces=true":
			return true
		}
	}
	return false
}

func isAllNamespaceGetArguments(arguments json.RawMessage) bool {
	var args namespaceListArgs
	return json.Unmarshal(arguments, &args) == nil && isAllNamespaceGet(args.Argv)
}

func parseNamespacedObjects(stdout, inferredKind string) []namespacedObject {
	var list kubernetesList
	if json.Unmarshal([]byte(stdout), &list) == nil && list.Items != nil {
		objects := make([]namespacedObject, 0, len(list.Items))
		for _, item := range list.Items {
			if item.Metadata.Name != "" && item.Metadata.Namespace != "" {
				kind := item.Kind
				if kind == "" {
					kind = inferredKind
				}
				normalizedKind, apiGroup := splitKindAndAPIGroup(kind)
				apiVersion := item.APIVersion
				if apiVersion == "" {
					apiVersion = list.APIVersion
				}
				if apiVersion != "" {
					if group, _, ok := strings.Cut(apiVersion, "/"); ok {
						apiGroup = normalizeAPIGroup(group)
					}
				}
				objects = append(objects, namespacedObject{
					Kind: normalizedKind, APIGroup: apiGroup,
					Name: normalizeObjectName(item.Metadata.Name), Namespace: item.Metadata.Namespace,
				})
			}
		}
		return objects
	}

	var objects []namespacedObject
	headerSeen := false
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.EqualFold(fields[0], "NAMESPACE") && strings.EqualFold(fields[1], "NAME") {
			headerSeen = true
			continue
		}
		if !headerSeen || strings.EqualFold(fields[0], "No") {
			continue
		}
		// kubectl get <multiple-resource-types> prints NAME as
		// resource.type/name (for example deployment.apps/demo-api).
		name := fields[1]
		kind := inferredKind
		if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
			kind = name[:slash]
			name = name[slash+1:]
		}
		normalizedKind, apiGroup := splitKindAndAPIGroup(kind)
		objects = append(objects, namespacedObject{
			Kind: normalizedKind, APIGroup: apiGroup,
			Name: normalizeObjectName(name), Namespace: fields[0],
		})
	}
	return objects
}

func fallbackKind(argv []string) string {
	if len(argv) < 2 {
		return ""
	}
	resourceList := strings.Split(argv[1], ",")
	if len(resourceList) != 1 {
		return ""
	}
	return strings.TrimSpace(resourceList[0])
}

func splitKindAndAPIGroup(kind string) (string, string) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if base, group, ok := strings.Cut(kind, "."); ok {
		return normalizeKind(base), normalizeAPIGroup(group)
	}
	return normalizeKind(kind), ""
}

func normalizeAPIGroup(group string) string {
	group = strings.ToLower(strings.TrimSpace(group))
	if base, _, ok := strings.Cut(group, "/"); ok {
		group = base
	}
	return group
}

func namespaceGroupIdentity(kind, apiGroup string) string {
	kind = normalizeKind(kind)
	apiGroup = normalizeAPIGroup(apiGroup)
	if apiGroup == "" {
		return kind
	}
	return kind + "." + apiGroup
}

func splitNamespaceGroupIdentity(identity string) (string, string) {
	return splitKindAndAPIGroup(identity)
}

func normalizeKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if dot := strings.IndexByte(kind, '.'); dot >= 0 {
		kind = kind[:dot]
	}
	aliases := map[string]string{
		"deploy": "deployment", "deployments": "deployment",
		"sts": "statefulset", "statefulsets": "statefulset",
		"ds": "daemonset", "daemonsets": "daemonset",
		"rs": "replicaset", "replicasets": "replicaset",
		"svc": "service", "services": "service",
		"po": "pod", "pods": "pod",
		"cm": "configmap", "configmaps": "configmap",
		"secrets": "secret", "pvc": "persistentvolumeclaim", "pvcs": "persistentvolumeclaim",
		"ingresses": "ingress", "jobs": "job", "cronjobs": "cronjob",
	}
	if normalized, ok := aliases[kind]; ok {
		return normalized
	}
	if strings.HasSuffix(kind, "ies") && len(kind) > 3 {
		return strings.TrimSuffix(kind, "ies") + "y"
	}
	if strings.HasSuffix(kind, "s") && !strings.HasSuffix(kind, "ss") && len(kind) > 1 {
		return strings.TrimSuffix(kind, "s")
	}
	return kind
}

func normalizeObjectName(name string) string {
	name = strings.TrimSpace(name)
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	return name
}

func matchingNamespaces(objects []namespacedObject, name, kind string) []string {
	groups := duplicateNamespaceGroups(objects, name, kind)
	seen := make(map[string]struct{})
	var namespaces []string
	for _, candidates := range groups {
		for _, namespace := range candidates {
			if _, ok := seen[namespace]; ok {
				continue
			}
			seen[namespace] = struct{}{}
			namespaces = append(namespaces, namespace)
		}
	}
	slices.Sort(namespaces)
	return namespaces
}

func duplicateNamespaceGroups(objects []namespacedObject, name, kind string) map[string][]string {
	return duplicateNamespaceGroupsByAPIGroup(objects, name, kind, "")
}

func duplicateNamespaceGroupsByAPIGroup(objects []namespacedObject, name, kind, apiGroup string) map[string][]string {
	name = normalizeObjectName(name)
	kind = normalizeKind(kind)
	apiGroup = normalizeAPIGroup(apiGroup)
	filterAPIGroup := apiGroup != ""
	if apiGroup == "@core" {
		apiGroup = ""
	}
	byKind := make(map[string]map[string]struct{})
	for _, object := range objects {
		objectKind := normalizeKind(object.Kind)
		objectGroup := normalizeAPIGroup(object.APIGroup)
		if normalizeObjectName(object.Name) != name || object.Namespace == "" ||
			kind != "" && objectKind != kind || filterAPIGroup && objectGroup != apiGroup {
			continue
		}
		identity := namespaceGroupIdentity(objectKind, objectGroup)
		if byKind[identity] == nil {
			byKind[identity] = make(map[string]struct{})
		}
		byKind[identity][object.Namespace] = struct{}{}
	}
	kindGroups := make(map[string]int)
	duplicatedKinds := make(map[string]struct{})
	for identity, namespaces := range byKind {
		objectKind, _ := splitNamespaceGroupIdentity(identity)
		kindGroups[objectKind]++
		if len(namespaces) > 1 {
			duplicatedKinds[objectKind] = struct{}{}
		}
	}
	groups := make(map[string][]string)
	for identity, candidates := range byKind {
		objectKind, _ := splitNamespaceGroupIdentity(identity)
		_, kindIsDuplicated := duplicatedKinds[objectKind]
		if len(candidates) < 2 && (kindGroups[objectKind] < 2 || !kindIsDuplicated) {
			continue
		}
		for namespace := range candidates {
			groups[identity] = append(groups[identity], namespace)
		}
		slices.Sort(groups[identity])
	}
	return groups
}

func namespaceCandidateOptions(groups map[string][]string) []string {
	kindCounts := make(map[string]int, len(groups))
	for identity := range groups {
		kind, _ := splitNamespaceGroupIdentity(identity)
		kindCounts[kind]++
	}
	identities := make([]string, 0, len(groups))
	for identity := range groups {
		identities = append(identities, identity)
	}
	slices.Sort(identities)
	var options []string
	for _, identity := range identities {
		kind, apiGroup := splitNamespaceGroupIdentity(identity)
		if kindCounts[kind] > 1 {
			if apiGroup == "" {
				// Kubernetes' core API group is empty. Mark it explicitly when a
				// same-Kind named API group is also a candidate so Resume can retain
				// the user's choice.
				kind += ".@core"
			} else {
				kind += "." + apiGroup
			}
		}
		for _, namespace := range groups[identity] {
			options = append(options, kind+"/"+namespace)
		}
	}
	return options
}

func queryResourceKind(node *core.Node) string {
	if node == nil {
		return ""
	}
	return normalizeKind(node.Attrs["k8s.kind"])
}

func namespaceLookupKind(node *core.Node, attrs map[string]string, targetType, name string) string {
	if isPodTarget(attrs, targetType) {
		if podName := normalizeObjectName(attrs["k8s.name"]); podName != "" && podWorkloadName(podName) == normalizeObjectName(name) {
			// The lookup name is the owning workload, not the literal Pod name.
			// Search across kinds so a Deployment/Service collision is not missed.
			return ""
		}
	}
	if kind := queryResourceKind(node); kind != "" {
		return kind
	}
	return normalizeKind(attrs["k8s.kind"])
}

func namespaceLookupAPIGroup(node *core.Node, attrs map[string]string, targetType, name string) string {
	if isPodTarget(attrs, targetType) {
		if podName := normalizeObjectName(attrs["k8s.name"]); podName != "" && podWorkloadName(podName) == normalizeObjectName(name) {
			return ""
		}
	}
	if group := normalizeAPIGroup(attrs["k8s.apiGroup"]); group != "" {
		return group
	}
	if _, group := splitKindAndAPIGroup(attrs["k8s.kind"]); group != "" {
		return group
	}
	if node != nil {
		if group := normalizeAPIGroup(node.Attrs["k8s.apiGroup"]); group != "" {
			return group
		}
		if _, group := splitKindAndAPIGroup(node.Attrs["k8s.kind"]); group != "" {
			return group
		}
	}
	return ""
}

func selectedNamespace(node *core.Node, selection string, namespaces []string) (string, bool) {
	if node != nil {
		if namespace := strings.TrimSpace(node.Attrs["k8s.namespace"]); slices.Contains(namespaces, namespace) {
			return namespace, true
		}
	}
	selection = strings.TrimSpace(selection)
	if slices.Contains(namespaces, selection) {
		return selection, true
	}
	return "", false
}

func (o *Orchestrator) discoverNamespaceAmbiguity(
	ctx context.Context,
	query core.Query,
	state *ResolveState,
	action ResolveAction,
) (*ClarifyRequest, error) {
	for targetIndex, target := range action.Targets {
		node := queryNodeByID(query, target.NodeID)
		if node == nil || strings.TrimSpace(node.Attrs["k8s.namespace"]) != "" {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(target.Type), "k8s.") && strings.TrimSpace(node.Attrs["k8s.name"]) == "" {
			continue
		}
		name := namespaceLookupName(node, target.Attrs, target.Type)
		if name == "" {
			continue
		}
		kind := namespaceLookupKind(node, target.Attrs, target.Type, name)
		apiGroup := namespaceLookupAPIGroup(node, target.Attrs, target.Type, name)
		if _, selected := namespaceSelectionForTarget(
			*state, target.NodeID, name, kind, apiGroup,
			hasSelectedKindSibling(query, action, targetIndex, name, kind, apiGroup, state.NamespaceSelections),
			len(action.Targets),
		); selected {
			continue
		}
		if hasNamespaceEvidenceFor(*state, name, kind, apiGroup) {
			continue
		}
		available, ok := o.executor.(interface{ HasTool(string) bool })
		if !ok || !available.HasTool("k8s") {
			continue
		}
		resource := namespaceScanResource(kind)
		if apiGroup != "" {
			// Built-in aliases such as "services" lose their API-group identity.
			// Resolve the fully qualified resource name from api-resources instead.
			resource = ""
		}
		var resourceBatches [][]namespaceResource
		if kind == "" || resource == "" {
			batches, err := o.discoverNamespaceResources(ctx, state, node.ID, name)
			if err != nil {
				return nil, err
			}
			if kind != "" {
				for _, batch := range batches {
					for _, candidate := range batch {
						if normalizeKind(candidate.Kind) == normalizeKind(kind) &&
							(apiGroup == "" || normalizeAPIGroup(candidate.APIGroup) == normalizeAPIGroup(apiGroup)) {
							// Same-Kind resources from different API groups are queried
							// separately because kubectl rejects duplicate kinds in one get.
							resourceBatches = append(resourceBatches, []namespaceResource{candidate})
						}
					}
				}
				if len(resourceBatches) == 0 {
					// A complete namespaced inventory with no matching kind means the
					// proposed object is cluster-scoped or absent from this cluster.
					continue
				}
			} else if apiGroup == "" {
				resourceBatches = batches
			} else {
				for _, batch := range batches {
					var matching []namespaceResource
					for _, candidate := range batch {
						if normalizeAPIGroup(candidate.APIGroup) == normalizeAPIGroup(apiGroup) {
							matching = append(matching, candidate)
						}
					}
					if len(matching) > 0 {
						resourceBatches = append(resourceBatches, matching)
					}
				}
			}
		} else {
			resourceBatches = [][]namespaceResource{{{
				Name: resource, Kind: kind, APIGroup: apiGroup,
			}}}
		}
		for _, batch := range resourceBatches {
			if len(batch) == 0 {
				continue
			}
			resourceNames := make([]string, 0, len(batch))
			for _, resource := range batch {
				resourceNames = append(resourceNames, resource.Name)
			}
			argv := []string{"get", strings.Join(resourceNames, ",")}
			// A multi-kind inventory is listed without a name, then matched locally.
			if kind != "" {
				argv = append(argv, name)
			}
			argv = append(argv, "--all-namespaces")
			if _, err := o.executeNamespaceCheck(ctx, state, node.ID, name,
				argv,
				"确认 Kubernetes 目标是否存在跨 namespace 同名资源"); err != nil {
				return nil, err
			}
		}
		if clarification := namespaceAmbiguity(query, *state, action); clarification != nil {
			rememberNamespaceClarification(state, clarification)
			return clarification, nil
		}
	}
	return nil, nil
}

func (o *Orchestrator) executeNamespaceCheck(
	ctx context.Context,
	state *ResolveState,
	nodeID string,
	targetName string,
	argv []string,
	purpose string,
) (namespaceListResult, error) {
	if state.Round >= state.MaxRounds {
		return namespaceListResult{}, fmt.Errorf("resolve budget exceeded before namespace uniqueness check for %q", targetName)
	}
	arguments, err := json.Marshal(namespaceListArgs{Argv: argv})
	if err != nil {
		return namespaceListResult{}, fmt.Errorf("encode namespace uniqueness query for %q: %w", targetName, err)
	}
	call := ProposedToolCall{ToolName: "k8s", Arguments: arguments, Purpose: purpose, Refs: []string{nodeID}}
	if err := o.applyToolCall(ctx, state, call); err != nil {
		return namespaceListResult{}, err
	}
	if len(state.Evidence) == 0 {
		return namespaceListResult{}, fmt.Errorf("namespace uniqueness check for %q produced no evidence", targetName)
	}
	check := state.Evidence[len(state.Evidence)-1]
	var result namespaceListResult
	if err := json.Unmarshal(check.Raw, &result); err != nil {
		return namespaceListResult{}, fmt.Errorf("decode namespace uniqueness check for %q: %w", targetName, err)
	}
	if check.Error != "" {
		if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
			return namespaceListResult{}, fmt.Errorf("namespace uniqueness check for %q failed: %s (%s)", targetName, check.Error, stderr)
		}
		return namespaceListResult{}, fmt.Errorf("namespace uniqueness check for %q failed: %s", targetName, check.Error)
	}
	if result.ExitCode != 0 {
		if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
			return namespaceListResult{}, fmt.Errorf("namespace uniqueness check for %q failed with exit code %d: %s", targetName, result.ExitCode, stderr)
		}
		return namespaceListResult{}, fmt.Errorf("namespace uniqueness check for %q failed with exit code %d", targetName, result.ExitCode)
	}
	if result.StdoutTruncated {
		return namespaceListResult{}, fmt.Errorf("namespace uniqueness check for %q was truncated", targetName)
	}
	return result, nil
}

func (o *Orchestrator) discoverNamespaceResources(
	ctx context.Context,
	state *ResolveState,
	nodeID string,
	targetName string,
) ([][]namespaceResource, error) {
	result, err := o.executeNamespaceCheck(ctx, state, nodeID, targetName,
		[]string{"api-resources", "--namespaced=true", "--verbs=list"},
		"发现命名空间级 Kubernetes 资源清单")
	if err != nil {
		return nil, fmt.Errorf("discover namespaced Kubernetes resources: %w", err)
	}
	return namespaceResourceBatches(result.Stdout, targetName)
}

func namespaceResourceNames(stdout, targetName string) ([]string, error) {
	batches, err := namespaceResourceBatches(stdout, targetName)
	if err != nil {
		return nil, err
	}
	var resources []string
	for _, batch := range batches {
		for _, resource := range batch {
			resources = append(resources, resource.Name)
		}
	}
	slices.Sort(resources)
	return resources, nil
}

func namespaceResourceBatches(stdout, targetName string) ([][]namespaceResource, error) {
	parsed := parseAPIResources(stdout)
	if len(parsed) >= 300 {
		return nil, fmt.Errorf("namespace resource inventory for %q may be incomplete", targetName)
	}
	seen := make(map[string]struct{}, len(parsed))
	var batches [][]namespaceResource
	var batchKinds []map[string]struct{}
	for _, resource := range parsed {
		if !resource.Namespaced || resource.Name == "" || strings.Contains(resource.Name, "/") {
			continue
		}
		name := resource.Name
		if resource.APIGroup != "" {
			name += "." + resource.APIGroup
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		kind := normalizeKind(resource.Kind)
		if kind == "" {
			kind, _ = splitKindAndAPIGroup(name)
		}
		entry := namespaceResource{Name: name, Kind: kind, APIGroup: normalizeAPIGroup(resource.APIGroup)}
		batchIndex := 0
		for ; batchIndex < len(batches); batchIndex++ {
			if _, found := batchKinds[batchIndex][kind]; !found {
				break
			}
		}
		if batchIndex == len(batches) {
			batches = append(batches, nil)
			batchKinds = append(batchKinds, make(map[string]struct{}))
		}
		batches[batchIndex] = append(batches[batchIndex], entry)
		batchKinds[batchIndex][kind] = struct{}{}
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("namespace resource inventory for %q is empty", targetName)
	}
	for _, batch := range batches {
		slices.SortFunc(batch, func(left, right namespaceResource) int {
			return strings.Compare(left.Name, right.Name)
		})
	}
	return batches, nil
}

func queryObjectName(node *core.Node) string {
	if node == nil {
		return ""
	}
	if name := strings.TrimSpace(node.Attrs["k8s.name"]); name != "" {
		name = normalizeObjectName(name)
		if isKubernetesObjectName(name) {
			return name
		}
		return ""
	}
	name := normalizeObjectName(node.Text)
	if !isKubernetesObjectName(name) {
		return ""
	}
	return name
}

func namespaceLookupName(node *core.Node, targetAttrs map[string]string, targetType string) string {
	if node != nil {
		if name := strings.TrimSpace(node.Attrs["k8s.name"]); name != "" {
			name = normalizeObjectName(name)
			if isKubernetesObjectName(name) {
				return name
			}
		}
	}
	textName := queryObjectName(node)
	for _, key := range []string{"k8s.workloadName", "k8s.ownerName", "k8s.lookupName", "k8s.name"} {
		if name := normalizeObjectName(targetAttrs[key]); isKubernetesObjectName(name) {
			if isPodTarget(targetAttrs, targetType) {
				if workload := podWorkloadName(name); workload != "" {
					if textName == workload {
						return textName
					}
					return workload
				}
			}
			if textName == name {
				return textName
			}
			return name
		}
	}
	return textName
}

func isPodTarget(attrs map[string]string, targetType string) bool {
	return normalizeKind(attrs["k8s.kind"]) == "pod" || strings.Contains(strings.ToLower(targetType), "pod")
}

func podWorkloadName(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) >= 2 && len(parts[len(parts)-1]) == 9 && isAlphaNumeric(parts[len(parts)-1]) {
		workload := strings.Join(parts[:len(parts)-1], "-")
		if isKubernetesObjectName(workload) {
			return workload
		}
	}
	if len(parts) >= 3 && len(parts[len(parts)-1]) == 5 && len(parts[len(parts)-2]) == 9 &&
		isAlphaNumeric(parts[len(parts)-1]) && isAlphaNumeric(parts[len(parts)-2]) {
		workload := strings.Join(parts[:len(parts)-2], "-")
		if isKubernetesObjectName(workload) {
			return workload
		}
	}
	if len(parts) >= 2 {
		last := parts[len(parts)-1]
		if last != "" && isDigits(last) {
			workload := strings.Join(parts[:len(parts)-1], "-")
			if isKubernetesObjectName(workload) {
				return workload
			}
		}
	}
	return ""
}

func isKubernetesObjectName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !isDNSLabel(label) {
			return false
		}
	}
	return true
}

func isDNSLabel(label string) bool {
	if label == "" || len(label) > 63 || !isAlphaNumeric(label[:1]) || !isAlphaNumeric(label[len(label)-1:]) {
		return false
	}
	for _, char := range label {
		if !(char == '-' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func isAlphaNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func namespaceScanResource(kind string) string {
	if kind != "" {
		resources := map[string]string{
			"deployment": "deployments", "statefulset": "statefulsets", "daemonset": "daemonsets",
			"replicaset": "replicasets", "service": "services", "pod": "pods", "configmap": "configmaps",
			"secret": "secrets", "persistentvolumeclaim": "persistentvolumeclaims", "ingress": "ingresses",
			"job": "jobs", "cronjob": "cronjobs",
		}
		if resource, ok := resources[normalizeKind(kind)]; ok {
			return resource
		}
		return ""
	}
	return ""
}

func hasNamespaceEvidenceFor(state ResolveState, name, kind, apiGroup string) bool {
	tasks := make(map[string]core.Task, len(state.Tasks))
	for _, task := range state.Tasks {
		if task.ID != "" {
			tasks[task.ID] = task
		}
	}
	for _, evidence := range state.Evidence {
		if evidence.ToolName != "k8s" || evidence.Error != "" {
			continue
		}
		task, ok := tasks[evidence.TaskID]
		if !ok || task.ToolName != "k8s" {
			continue
		}
		var args namespaceListArgs
		if json.Unmarshal(task.Arguments, &args) != nil ||
			!isCompleteNamespaceLookup(args.Argv, name, kind, apiGroup) && !hasCompleteNamespaceInventoryLookup(state, args.Argv) {
			continue
		}
		var result namespaceListResult
		if json.Unmarshal(evidence.Raw, &result) != nil || result.ExitCode != 0 || result.StdoutTruncated || !hasParsableNamespacedList(result.Stdout) {
			continue
		}
		return true
	}
	return false
}

func hasCompleteNamespaceInventoryLookup(state ResolveState, argv []string) bool {
	if !isBroadNamespaceResourceList(argv) {
		return false
	}

	tasks := make(map[string]core.Task, len(state.Tasks))
	for _, task := range state.Tasks {
		if task.ID != "" {
			tasks[task.ID] = task
		}
	}
	var inventory []string
	for _, evidence := range state.Evidence {
		if evidence.ToolName != "k8s" || evidence.Error != "" {
			continue
		}
		task, ok := tasks[evidence.TaskID]
		if !ok || task.ToolName != "k8s" {
			continue
		}
		var args namespaceListArgs
		if json.Unmarshal(task.Arguments, &args) != nil || !isNamespacedListResourceInventory(args.Argv) {
			continue
		}
		var result namespaceListResult
		if json.Unmarshal(evidence.Raw, &result) != nil || result.ExitCode != 0 || result.StdoutTruncated {
			continue
		}
		var err error
		inventory, err = namespaceResourceNames(result.Stdout, "inventory")
		if err != nil {
			continue
		}
		break
	}
	if len(inventory) == 0 {
		return false
	}
	got := make(map[string]struct{}, len(inventory))
	for _, resource := range strings.Split(argv[1], ",") {
		got[strings.ToLower(resource)] = struct{}{}
	}
	for _, evidence := range state.Evidence {
		if evidence.ToolName != "k8s" || evidence.Error != "" {
			continue
		}
		task, ok := tasks[evidence.TaskID]
		if !ok || task.ToolName != "k8s" {
			continue
		}
		var args namespaceListArgs
		if json.Unmarshal(task.Arguments, &args) != nil || !isBroadNamespaceResourceList(args.Argv) {
			continue
		}
		var result namespaceListResult
		if json.Unmarshal(evidence.Raw, &result) != nil || result.ExitCode != 0 || result.StdoutTruncated || !hasParsableNamespacedList(result.Stdout) {
			continue
		}
		for _, resource := range strings.Split(args.Argv[1], ",") {
			got[strings.ToLower(resource)] = struct{}{}
		}
	}
	if len(got) != len(inventory) {
		return false
	}
	for _, resource := range inventory {
		if _, ok := got[strings.ToLower(resource)]; !ok {
			return false
		}
	}
	return true
}

func hasParsableNamespacedList(stdout string) bool {
	var list kubernetesList
	if json.Unmarshal([]byte(stdout), &list) == nil && list.Items != nil {
		return true
	}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "NAMESPACE") && strings.EqualFold(fields[1], "NAME") {
			return true
		}
		if len(fields) >= 3 && strings.EqualFold(fields[0], "No") && strings.EqualFold(fields[1], "resources") && strings.EqualFold(fields[2], "found") {
			return true
		}
	}
	return false
}

func isNamespacedListResourceInventory(argv []string) bool {
	return len(argv) == 3 && strings.EqualFold(argv[0], "api-resources") &&
		strings.EqualFold(argv[1], "--namespaced=true") && strings.EqualFold(argv[2], "--verbs=list")
}

func isBroadNamespaceResourceList(argv []string) bool {
	if len(argv) != 3 || !strings.EqualFold(argv[0], "get") || strings.TrimSpace(argv[1]) == "" || strings.Contains(argv[1], "/") {
		return false
	}
	switch strings.ToLower(argv[2]) {
	case "-a", "--all-namespaces", "--all-namespaces=true":
		return true
	default:
		return false
	}
}

func isCompleteNamespaceLookup(argv []string, name, kind, apiGroup string) bool {
	if !isAllNamespaceGet(argv) || len(argv) < 2 {
		return false
	}
	resources := strings.Split(argv[1], ",")
	resourceSet := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		resourceKind, resourceGroup := splitKindAndAPIGroup(resource)
		resourceSet[namespaceGroupIdentity(resourceKind, resourceGroup)] = struct{}{}
	}
	if kind != "" {
		if _, ok := resourceSet[namespaceGroupIdentity(kind, apiGroup)]; !ok {
			if apiGroup != "" {
				return false
			}
			if _, all := resourceSet["all"]; !all || !kindIncludedByAll(kind) {
				return false
			}
		}
	} else {
		// Without a known kind, completeness requires an inventory query whose
		// resource list matches the full api-resources result.
		return false
	}

	// A named get is complete only for this exact object name. An unnamed list
	// is also complete because it returns every object of the selected kinds.
	for i := 2; i < len(argv); i++ {
		arg := argv[i]
		if arg == "-l" || arg == "--selector" || arg == "--field-selector" ||
			strings.HasPrefix(arg, "-l=") || strings.HasPrefix(arg, "--selector=") || strings.HasPrefix(arg, "--field-selector=") ||
			arg == "-n" || arg == "--namespace" || strings.HasPrefix(arg, "-n=") || strings.HasPrefix(arg, "--namespace=") {
			return false
		}
		if arg == "-o" || arg == "--output" {
			if i+1 >= len(argv) {
				return false
			}
			format := strings.ToLower(strings.TrimSpace(argv[i+1]))
			if format != "wide" && format != "json" {
				return false
			}
			i++
			continue
		}
		if strings.HasPrefix(arg, "--output=") {
			format := strings.TrimPrefix(strings.ToLower(arg), "--output=")
			if format != "wide" && format != "json" {
				return false
			}
			continue
		}
		if arg == "-A" || arg == "--all-namespaces" || arg == "--all-namespaces=true" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return false
		}
		if arg != normalizeObjectName(name) {
			return false
		}
	}
	return true
}

func kindIncludedByAll(kind string) bool {
	switch normalizeKind(kind) {
	case "deployment", "statefulset", "daemonset", "replicaset", "service", "pod", "job", "cronjob":
		return true
	default:
		return false
	}
}

func parseNamespaceSelection(answer string, options []string) string {
	var selected string
	for _, option := range options {
		matches := containsNamespaceMention(answer, option)
		if kind, namespace, ok := strings.Cut(option, "/"); ok {
			matches = matches || containsNamespaceMention(answer, kind) && containsNamespaceMention(answer, namespace)
		}
		if !matches {
			continue
		}
		if selected != "" {
			return ""
		}
		selected = option
	}
	return selected
}

func containsNamespaceMention(text, namespace string) bool {
	text = strings.ToLower(text)
	namespace = strings.ToLower(namespace)
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], namespace)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(namespace)
		leftBoundary := start == 0 || !isNamespaceNameRune(rune(text[start-1]))
		rightBoundary := end == len(text) || !isNamespaceNameRune(rune(text[end]))
		if leftBoundary && rightBoundary {
			return true
		}
		offset = start + 1
	}
	return false
}

func isNamespaceNameRune(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' || char == '.'
}
