package worldradar

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GraphNode represents a vertex in the World Radar entity-event knowledge graph.
type GraphNode struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Label       string  `json:"label"`
	Domain      string  `json:"domain"`
	ReviewState string  `json:"reviewState"`
	Weight      float64 `json:"weight"`
}

// GraphEdge represents a directed, evidence-backed relationship in the knowledge graph.
type GraphEdge struct {
	ID                 string  `json:"id"`
	Source             string  `json:"source"`
	Target             string  `json:"target"`
	Predicate          string  `json:"predicate"`
	Confidence         float64 `json:"confidence"`
	EvidenceArtifactID string  `json:"evidenceArtifactId"`
}

// EntityGraph bundles vertices and edges for visualization and graph traversal.
type EntityGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// DiscoveryProposal represents a proposed watch target awaiting operator approval or rejection.
type DiscoveryProposal struct {
	ID                string `json:"id"`
	InstanceID        string `json:"instanceId"`
	TargetType        string `json:"targetType"`
	CanonicalName     string `json:"canonicalName"`
	ConnectorID       string `json:"connectorId"`
	SourceURI         string `json:"sourceUri"`
	IndependenceGroup string `json:"independenceGroup"`
	Reason            string `json:"reason"`
	Status            string `json:"status"`
	DecisionReason    string `json:"decisionReason"`
	CreatedAt         string `json:"createdAt"`
	DecidedAt         string `json:"decidedAt"`
}

// ProposalInput specifies fields to propose a new discovery watch target.
type ProposalInput struct {
	InstanceID        string `json:"instanceId"`
	TargetType        string `json:"targetType"`
	CanonicalName     string `json:"canonicalName"`
	ConnectorID       string `json:"connectorId"`
	SourceURI         string `json:"sourceUri"`
	IndependenceGroup string `json:"independenceGroup,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

// BuildEntityGraph constructs the entity-event knowledge graph up to limit edges/nodes.
func (e *Engine) BuildEntityGraph(ctx context.Context, limit int) (EntityGraph, error) {
	limit = normalizeLimit(limit, 100, 500)

	relRows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, subject_entity_id, predicate, object_entity_id, confidence, evidence_artifact_id
FROM radar_relationships
ORDER BY created_at DESC
LIMIT ?`, limit)
	if err != nil {
		return EntityGraph{}, err
	}
	defer relRows.Close()

	edges := make([]GraphEdge, 0)
	nodeDegree := make(map[string]int)
	for relRows.Next() {
		var edge GraphEdge
		if scanErr := relRows.Scan(
			&edge.ID, &edge.Source, &edge.Target, &edge.Predicate, &edge.Confidence, &edge.EvidenceArtifactID,
		); scanErr != nil {
			return EntityGraph{}, scanErr
		}
		edges = append(edges, edge)
		nodeDegree[edge.Source]++
		nodeDegree[edge.Target]++
	}
	if err := relRows.Err(); err != nil {
		return EntityGraph{}, err
	}

	nodeMap := make(map[string]GraphNode)

	entRows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, canonical_name, review_state
FROM radar_entities
ORDER BY created_at DESC
LIMIT ?`, limit)
	if err != nil {
		return EntityGraph{}, err
	}
	defer entRows.Close()
	for entRows.Next() {
		var id, name, reviewState string
		if scanErr := entRows.Scan(&id, &name, &reviewState); scanErr != nil {
			return EntityGraph{}, scanErr
		}
		deg := nodeDegree[id]
		nodeMap[id] = GraphNode{
			ID:          id,
			Kind:        "entity",
			Label:       name,
			Domain:      "",
			ReviewState: reviewState,
			Weight:      1.0 + float64(deg)*0.5,
		}
	}
	if err := entRows.Err(); err != nil {
		return EntityGraph{}, err
	}

	evtRows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, title, primary_domain, status
FROM radar_events
ORDER BY updated_at DESC, created_at DESC
LIMIT ?`, limit)
	if err != nil {
		return EntityGraph{}, err
	}
	defer evtRows.Close()
	for evtRows.Next() {
		var id, title, primaryDomain, status string
		if scanErr := evtRows.Scan(&id, &title, &primaryDomain, &status); scanErr != nil {
			return EntityGraph{}, scanErr
		}
		deg := nodeDegree[id]
		nodeMap[id] = GraphNode{
			ID:          id,
			Kind:        "event",
			Label:       title,
			Domain:      primaryDomain,
			ReviewState: status,
			Weight:      1.0 + float64(deg)*0.5,
		}
	}
	if err := evtRows.Err(); err != nil {
		return EntityGraph{}, err
	}

	// Ensure every endpoint referenced by an edge exists in nodeMap even if outside recent LIMIT.
	for _, edge := range edges {
		if _, ok := nodeMap[edge.Source]; !ok {
			if node, found := e.lookupGraphNode(ctx, edge.Source, nodeDegree[edge.Source]); found {
				nodeMap[edge.Source] = node
			}
		}
		if _, ok := nodeMap[edge.Target]; !ok {
			if node, found := e.lookupGraphNode(ctx, edge.Target, nodeDegree[edge.Target]); found {
				nodeMap[edge.Target] = node
			}
		}
	}

	// Propagate primary domain to entity nodes that lack a direct domain when linked to an event node.
	for _, edge := range edges {
		src, hasSrc := nodeMap[edge.Source]
		tgt, hasTgt := nodeMap[edge.Target]
		if hasSrc && hasTgt && src.Domain == "" && tgt.Domain != "" {
			src.Domain = tgt.Domain
			nodeMap[edge.Source] = src
		}
	}

	nodes := make([]GraphNode, 0, len(nodeMap))
	for _, node := range nodeMap {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Weight != nodes[j].Weight {
			return nodes[i].Weight > nodes[j].Weight
		}
		return nodes[i].ID < nodes[j].ID
	})
	if len(nodes) > limit {
		nodes = nodes[:limit]
	}

	return EntityGraph{
		Nodes: nodes,
		Edges: edges,
	}, nil
}

func (e *Engine) lookupGraphNode(ctx context.Context, id string, degree int) (GraphNode, bool) {
	var name, reviewState string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT canonical_name, review_state FROM radar_entities WHERE id = ?`, id,
	).Scan(&name, &reviewState)
	if err == nil {
		return GraphNode{
			ID:          id,
			Kind:        "entity",
			Label:       name,
			ReviewState: reviewState,
			Weight:      1.0 + float64(degree)*0.5,
		}, true
	}

	var title, domain, status string
	err = e.hub.DB().QueryRowContext(ctx, `
SELECT title, primary_domain, status FROM radar_events WHERE id = ?`, id,
	).Scan(&title, &domain, &status)
	if err == nil {
		return GraphNode{
			ID:          id,
			Kind:        "event",
			Label:       title,
			Domain:      domain,
			ReviewState: status,
			Weight:      1.0 + float64(degree)*0.5,
		}, true
	}
	return GraphNode{}, false
}

// ProposeTarget submits a watch-target discovery proposal for an instance.
// If a proposal for (instance_id, source_uri) already exists (including 'rejected'),
// it returns the existing row untouched and NEVER resets 'rejected' back to 'pending'.
func (e *Engine) ProposeTarget(ctx context.Context, input ProposalInput) (DiscoveryProposal, error) {
	instanceID := strings.TrimSpace(input.InstanceID)
	if instanceID == "" {
		return DiscoveryProposal{}, fmt.Errorf("%w: instanceId is required", ErrValidation)
	}
	if _, err := e.GetInstance(ctx, instanceID); err != nil {
		return DiscoveryProposal{}, err
	}

	connectorID := strings.ToLower(strings.TrimSpace(input.ConnectorID))
	if _, ok := e.connectors.Get(connectorID); !ok {
		return DiscoveryProposal{}, fmt.Errorf("%w: unknown connector %q", ErrValidation, input.ConnectorID)
	}

	sourceURI := strings.TrimSpace(input.SourceURI)
	if sourceURI == "" {
		return DiscoveryProposal{}, fmt.Errorf("%w: sourceUri is required", ErrValidation)
	}

	existing, err := e.getProposalByInstanceAndURI(ctx, instanceID, sourceURI)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return DiscoveryProposal{}, err
	}

	targetType := firstNonEmpty(strings.TrimSpace(input.TargetType), "feed")
	canonicalName := firstNonEmpty(strings.TrimSpace(input.CanonicalName), sourceURI)
	indGroup := strings.ToLower(strings.TrimSpace(input.IndependenceGroup))
	if indGroup == "" {
		indGroup = deriveIndependenceGroup(sourceURI, connectorID)
	}
	reason := strings.TrimSpace(input.Reason)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	propID := makeID("prop", instanceID, connectorID, sourceURI, now)

	_, err = e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_discovery_proposals
  (id, instance_id, target_type, canonical_name, connector_id, source_uri, independence_group, reason, status, decision_reason, created_at, decided_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', '', ?, '')`,
		propID, instanceID, targetType, canonicalName, connectorID, sourceURI, indGroup, reason, now,
	)
	if err != nil {
		// Handle concurrent insert on UNIQUE(instance_id, source_uri) by returning existing row.
		if raced, raceErr := e.getProposalByInstanceAndURI(ctx, instanceID, sourceURI); raceErr == nil {
			return raced, nil
		}
		return DiscoveryProposal{}, fmt.Errorf("insert discovery proposal: %w", err)
	}

	e.writeAudit(ctx, "system", "propose_target", "radar_discovery_proposal", propID, "pending")
	return e.getProposalByID(ctx, propID)
}

// ListProposals lists discovery proposals optionally filtered by instanceID and status.
func (e *Engine) ListProposals(ctx context.Context, instanceID, status string) ([]DiscoveryProposal, error) {
	query := `
SELECT id, instance_id, target_type, canonical_name, connector_id, source_uri, independence_group, reason, status, decision_reason, created_at, decided_at
FROM radar_discovery_proposals`
	var clauses []string
	var args []any
	if cleanInst := strings.TrimSpace(instanceID); cleanInst != "" {
		clauses = append(clauses, "instance_id = ?")
		args = append(args, cleanInst)
	}
	if cleanStatus := strings.ToLower(strings.TrimSpace(status)); cleanStatus != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, cleanStatus)
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC"

	rows, err := e.hub.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	proposals := make([]DiscoveryProposal, 0)
	for rows.Next() {
		prop, scanErr := scanProposal(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		proposals = append(proposals, prop)
	}
	return proposals, rows.Err()
}

// ApproveProposal transitions a pending proposal to approved, attaches the watch target, and logs an audit entry.
func (e *Engine) ApproveProposal(ctx context.Context, proposalID string) (DiscoveryProposal, error) {
	prop, err := e.getProposalByID(ctx, proposalID)
	if err != nil {
		return DiscoveryProposal{}, err
	}
	if prop.Status == "approved" {
		return prop, nil
	}
	if prop.Status == "rejected" {
		return DiscoveryProposal{}, fmt.Errorf("%w: proposal %q was already rejected", ErrValidation, proposalID)
	}

	if _, err := e.AddWatchTarget(ctx, prop.InstanceID, WatchTargetInput{
		TargetType:        prop.TargetType,
		CanonicalName:     prop.CanonicalName,
		ConnectorID:       prop.ConnectorID,
		SourceURI:         prop.SourceURI,
		IndependenceGroup: prop.IndependenceGroup,
	}); err != nil {
		return DiscoveryProposal{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = e.hub.DB().ExecContext(ctx, `
UPDATE radar_discovery_proposals
SET status = 'approved', decision_reason = '', decided_at = ?
WHERE id = ?`, now, prop.ID)
	if err != nil {
		return DiscoveryProposal{}, err
	}

	e.writeAudit(ctx, "operator", "approve_proposal", "radar_discovery_proposal", prop.ID, "approved")
	return e.getProposalByID(ctx, prop.ID)
}

// RejectProposal marks a discovery proposal as rejected with a decision reason and logs an audit entry.
func (e *Engine) RejectProposal(ctx context.Context, proposalID, reason string) (DiscoveryProposal, error) {
	prop, err := e.getProposalByID(ctx, proposalID)
	if err != nil {
		return DiscoveryProposal{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = e.hub.DB().ExecContext(ctx, `
UPDATE radar_discovery_proposals
SET status = 'rejected', decision_reason = ?, decided_at = ?
WHERE id = ?`, strings.TrimSpace(reason), now, prop.ID)
	if err != nil {
		return DiscoveryProposal{}, err
	}

	e.writeAudit(ctx, "operator", "reject_proposal", "radar_discovery_proposal", prop.ID, "rejected")
	return e.getProposalByID(ctx, prop.ID)
}

func (e *Engine) getProposalByID(ctx context.Context, proposalID string) (DiscoveryProposal, error) {
	row := e.hub.DB().QueryRowContext(ctx, `
SELECT id, instance_id, target_type, canonical_name, connector_id, source_uri, independence_group, reason, status, decision_reason, created_at, decided_at
FROM radar_discovery_proposals WHERE id = ?`, strings.TrimSpace(proposalID))
	prop, err := scanProposal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DiscoveryProposal{}, fmt.Errorf("%w: proposal %q", ErrNotFound, proposalID)
	}
	return prop, err
}

func (e *Engine) getProposalByInstanceAndURI(ctx context.Context, instanceID, sourceURI string) (DiscoveryProposal, error) {
	row := e.hub.DB().QueryRowContext(ctx, `
SELECT id, instance_id, target_type, canonical_name, connector_id, source_uri, independence_group, reason, status, decision_reason, created_at, decided_at
FROM radar_discovery_proposals WHERE instance_id = ? AND source_uri = ?`, instanceID, sourceURI)
	prop, err := scanProposal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DiscoveryProposal{}, fmt.Errorf("%w: proposal for %s", ErrNotFound, sourceURI)
	}
	return prop, err
}

func scanProposal(scanner rowScanner) (DiscoveryProposal, error) {
	var prop DiscoveryProposal
	err := scanner.Scan(
		&prop.ID, &prop.InstanceID, &prop.TargetType, &prop.CanonicalName,
		&prop.ConnectorID, &prop.SourceURI, &prop.IndependenceGroup, &prop.Reason,
		&prop.Status, &prop.DecisionReason, &prop.CreatedAt, &prop.DecidedAt,
	)
	if err != nil {
		return DiscoveryProposal{}, err
	}
	return prop, nil
}
