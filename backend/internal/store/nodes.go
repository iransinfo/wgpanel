package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNodeNameTaken      = errors.New("node name already exists")
	ErrNodeNotFound       = errors.New("node not found")
	ErrInvalidOrUsedToken = errors.New("join token is invalid, expired, or already used")
)

type Node struct {
	ID                  string
	Name                string
	NodeGroup           string
	Region              string // free-form steering label (migration 0017); "" = unassigned
	PublicEndpoint      string
	WGSubnet            string
	CapacityMaxPeers    int
	Status              string
	PublicKey           *string // the node's own WireGuard server public key - see migration 0006
	JoinTokenExpiresAt  *time.Time
	MTLSCertFingerprint *string    // see migration 0007 / STORY-04
	LastHeartbeatAt     *time.Time // see migration 0007 / STORY-04
	CreatedAt           time.Time
}

const nodeColumns = `id, name, node_group, region, public_endpoint, wg_subnet, capacity_max_peers, status, public_key, join_token_expires_at, mtls_cert_fingerprint, last_heartbeat_at, created_at`

func scanNode(row pgx.Row, n *Node) error {
	return row.Scan(
		&n.ID, &n.Name, &n.NodeGroup, &n.Region, &n.PublicEndpoint, &n.WGSubnet,
		&n.CapacityMaxPeers, &n.Status, &n.PublicKey, &n.JoinTokenExpiresAt,
		&n.MTLSCertFingerprint, &n.LastHeartbeatAt, &n.CreatedAt,
	)
}

func (s *Store) CreateNode(ctx context.Context, name, nodeGroup, region, publicEndpoint, wgSubnet string, capacityMaxPeers int, publicKey *string) (Node, error) {
	var n Node
	err := scanNode(s.pool.QueryRow(ctx, `
		INSERT INTO nodes (name, node_group, region, public_endpoint, wg_subnet, capacity_max_peers, public_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+nodeColumns,
		name, nodeGroup, region, publicEndpoint, wgSubnet, capacityMaxPeers, publicKey,
	), &n)
	if err != nil {
		if isUniqueViolation(err) {
			return Node{}, ErrNodeNameTaken
		}
		return Node{}, err
	}
	return n, nil
}

// UpdateNodeParams: a nil field leaves that column unchanged, matching the same
// "omitted = unchanged" PATCH convention PATCH /api/v1/accounts/{id} and PATCH
// /api/v1/settings already use. Deliberately does not allow changing wg_subnet once
// peers may already be allocated from it - out of scope here (would need to
// re-derive every account_peers.assigned_ip on that node, a much bigger operation).
type UpdateNodeParams struct {
	Name             *string
	NodeGroup        *string
	Region           *string // pointer-to-"" explicitly clears the region (unlike the other fields, "" is a meaningful value here)
	PublicEndpoint   *string
	CapacityMaxPeers *int
}

func (s *Store) UpdateNode(ctx context.Context, id string, p UpdateNodeParams) (Node, error) {
	var n Node
	err := scanNode(s.pool.QueryRow(ctx, `
		UPDATE nodes SET
			name = COALESCE($2, name),
			node_group = COALESCE($3, node_group),
			region = COALESCE($4, region),
			public_endpoint = COALESCE($5, public_endpoint),
			capacity_max_peers = COALESCE($6, capacity_max_peers)
		WHERE id = $1
		RETURNING `+nodeColumns,
		id, p.Name, p.NodeGroup, p.Region, p.PublicEndpoint, p.CapacityMaxPeers,
	), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNodeNotFound
	}
	if err != nil && isUniqueViolation(err) {
		return Node{}, ErrNodeNameTaken
	}
	return n, err
}

func (s *Store) GetNode(ctx context.Context, id string) (Node, error) {
	var n Node
	err := scanNode(s.pool.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id = $1`, id), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNodeNotFound
	}
	return n, err
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+nodeColumns+` FROM nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		if err := scanNode(rows, &n); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// SetJoinToken stores only the SHA-256 hash of the raw token, overwriting any
// previous token for this node (the old one stops working immediately).
// SetJoinToken stores a normal, single-use, expiring join token. See
// SetUnlimitedJoinToken for the reusable/non-expiring variant.
func (s *Store) SetJoinToken(ctx context.Context, nodeID, rawToken string, expiresAt time.Time) error {
	hash := hashToken(rawToken)
	tag, err := s.pool.Exec(ctx, `
		UPDATE nodes SET join_token_hash = $1, join_token_expires_at = $2, join_token_unlimited = false
		WHERE id = $3
	`, hash, expiresAt, nodeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// SetUnlimitedJoinToken stores a reusable, non-expiring join token - meant for
// re-registering an already-registered node's agent (rebuilt container, replaced
// hardware, rotated identity) without the "must currently be pending, redeems once"
// restriction a normal token has. join_token_expires_at is left NULL - RedeemJoinToken
// skips the expiry/status check entirely when join_token_unlimited is true.
func (s *Store) SetUnlimitedJoinToken(ctx context.Context, nodeID, rawToken string) error {
	hash := hashToken(rawToken)
	tag, err := s.pool.Exec(ctx, `
		UPDATE nodes SET join_token_hash = $1, join_token_expires_at = NULL, join_token_unlimited = true
		WHERE id = $2
	`, hash, nodeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// RedeemJoinToken atomically transitions a node to registered if, and only if, the
// token matches - plus, for a normal (non-unlimited) token, that it hasn't expired
// and the node is still pending. The WHERE clause doing these checks in a single
// UPDATE is what makes this safe under concurrent redemption attempts - Postgres's
// row-level locking means only one concurrent UPDATE against the same row can
// succeed.
//
// An unlimited token (join_token_unlimited, see SetUnlimitedJoinToken) skips the
// pending/expiry requirement entirely and is NOT consumed on success - it stays
// valid for re-registering the same node again later (rebuilt container, replaced
// hardware, rotated agent identity), which a normal single-use token can't do
// without an operator manually resetting the node back to 'pending' first.
//
// Once registered, every currently-active account gets backfilled a peer on this
// node too (docs/STORY-09-multi-node-accounts.md) - a node joining the fleet later
// than an account was created shouldn't mean that account never syncs to it. This
// runs in its own transaction, separate from the UPDATE above; a crash between the
// two leaves the node registered but not yet backfilled, which is recoverable (the
// backfill query is idempotent - it only inserts peers that don't already exist) but
// there's no standing retry mechanism today if that exact crash window is hit.
func (s *Store) RedeemJoinToken(ctx context.Context, rawToken string) (Node, error) {
	hash := hashToken(rawToken)
	var n Node
	err := scanNode(s.pool.QueryRow(ctx, `
		UPDATE nodes
		SET status = 'registered',
			join_token_hash = CASE WHEN join_token_unlimited THEN join_token_hash ELSE NULL END,
			join_token_expires_at = CASE WHEN join_token_unlimited THEN join_token_expires_at ELSE NULL END
		WHERE join_token_hash = $1
			AND (join_token_unlimited OR (status = 'pending' AND join_token_expires_at > now()))
		RETURNING `+nodeColumns,
		hash,
	), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrInvalidOrUsedToken
	}
	if err != nil {
		return Node{}, err
	}

	if err := s.BackfillAccountPeersForNode(ctx, n.ID); err != nil {
		return Node{}, err
	}
	return n, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// SetNodePublicKey records the node's own WireGuard server public key, submitted by
// the agent during /agent/register once install-node.sh started actually creating a
// real WireGuard interface (setup_wireguard() there) instead of just installing the
// agent. Only overwrites when a non-empty key is provided - a re-registration
// without one (e.g. an older agent build) shouldn't blank out a key set previously.
func (s *Store) SetNodePublicKey(ctx context.Context, nodeID, publicKey string) error {
	if publicKey == "" {
		return nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE nodes SET public_key = $1 WHERE id = $2`, publicKey, nodeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// RecordNodeCertFingerprint pins the certificate issued during /agent/register so
// later heartbeats can be checked against the specific cert issued for this node,
// not just "signed by our CA" (docs/STORY-04-node-agent-mtls.md).
func (s *Store) RecordNodeCertFingerprint(ctx context.Context, nodeID, fingerprint string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE nodes SET mtls_cert_fingerprint = $1 WHERE id = $2`, fingerprint, nodeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// ErrFingerprintMismatch means the presented client certificate doesn't match the
// one pinned for this node id - e.g. a stale certificate from before a re-registration.
var ErrFingerprintMismatch = errors.New("certificate fingerprint does not match the one on record for this node")

// RecordHeartbeat verifies the presented certificate's fingerprint matches what was
// pinned at registration, then updates last_heartbeat_at and flips status to online.
func (s *Store) RecordHeartbeat(ctx context.Context, nodeID, presentedFingerprint string) error {
	var storedFingerprint *string
	err := s.pool.QueryRow(ctx, `SELECT mtls_cert_fingerprint FROM nodes WHERE id = $1`, nodeID).Scan(&storedFingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNodeNotFound
	}
	if err != nil {
		return err
	}
	if storedFingerprint == nil || *storedFingerprint != presentedFingerprint {
		return ErrFingerprintMismatch
	}

	_, err = s.pool.Exec(ctx, `
		UPDATE nodes SET status = 'online', last_heartbeat_at = now() WHERE id = $1
	`, nodeID)
	return err
}

// SweepOfflineNodes flips any 'online' node whose last heartbeat is older than
// staleAfter to 'offline', and returns how many were flipped (for logging - not an
// error condition, just an observability signal).
func (s *Store) SweepOfflineNodes(ctx context.Context, staleAfter time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE nodes SET status = 'offline'
		WHERE status = 'online' AND last_heartbeat_at < now() - make_interval(secs => $1)
	`, staleAfter.Seconds())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
