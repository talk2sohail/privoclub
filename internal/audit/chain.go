package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// The tamper-evident chain (ADR 0006, 0025): a sealer puts new events into
// blocks, each covering the previous one, and signs each block with a key
// derived from the master key, which never touches the database.

const signingSalt = "privo audit signing v1"

// SigningKey derives an org's Ed25519 signing key from a master key, and
// returns it with its id (the first 16 hex characters of the SHA-256 of the
// public key).
func SigningKey(masterKey []byte, org store.OrgID) (ed25519.PrivateKey, string, error) {
	if len(masterKey) == 0 {
		return nil, "", errors.New("audit: no master key")
	}
	seed, err := hkdf.Key(sha256.New, masterKey, []byte(signingSalt), string(org), ed25519.SeedSize)
	if err != nil {
		return nil, "", err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv, KeyID(priv.Public().(ed25519.PublicKey)), nil
}

// KeyID names a public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

// TrustedKeys are the public keys a verifier accepts, by key id.
type TrustedKeys map[string]ed25519.PublicKey

// KeysFrom derives the trusted keys of an org from the key provider: the
// current master key's and, during a rotation, the previous one's.
func KeysFrom(p vault.KeyProvider, org store.OrgID) (TrustedKeys, error) {
	out := TrustedKeys{}
	for _, k := range [][]byte{p.Current(), p.Previous()} {
		if len(k) == 0 {
			continue
		}
		priv, id, err := SigningKey(k, org)
		if err != nil {
			return nil, err
		}
		out[id] = priv.Public().(ed25519.PublicKey)
	}
	if len(out) == 0 {
		return nil, errors.New("audit: no master key")
	}
	return out, nil
}

// chainedFields is what the chain covers of an event, in a fixed order. The
// payload is covered by its digest, so erasing it keeps the chain valid.
type chainedFields struct {
	Org        string `json:"org"`
	Seq        int64  `json:"seq"`
	ID         string `json:"id"`
	CallID     string `json:"call_id"`
	Phase      string `json:"phase"`
	Time       string `json:"ts"`
	Actor      string `json:"actor"`
	ActorID    string `json:"actor_id"`
	Client     string `json:"client"`
	Server     string `json:"server"`
	Item       string `json:"item"`
	Action     string `json:"action"`
	Decision   string `json:"decision"`
	ApprovedBy string `json:"approved_by"`
	Account    string `json:"account"`
	Outcome    string `json:"outcome"`
	LatencyMS  int64  `json:"latency_ms"`
	Payload    string `json:"payload_sha256"`
}

// EventHash is the hex SHA-256 of an event's chained fields.
func EventHash(org store.OrgID, e store.ChainedEvent) string {
	b, _ := json.Marshal(chainedFields{Org: string(org), Seq: e.Seq, ID: e.ID, CallID: e.CallID, Phase: e.Phase,
		Time: e.Time.UTC().Format(time.RFC3339Nano), Actor: e.Actor, ActorID: e.ActorID, Client: e.Client, Server: e.Server, Item: e.Item,
		Action: e.Action, Decision: e.Decision, ApprovedBy: e.ApprovedBy, Account: e.Account, Outcome: e.Outcome, LatencyMS: e.LatencyMS,
		Payload: e.PayloadSHA256})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BlockHash is the hex SHA-256 of a block: the org, its number, the previous
// block's hash, its range and its events' hashes in order.
func BlockHash(org store.OrgID, seq, first, last int64, prev string, eventHashes []string) string {
	b, _ := json.Marshal(struct {
		Org    string   `json:"org"`
		Seq    int64    `json:"seq"`
		Prev   string   `json:"prev"`
		First  int64    `json:"first"`
		Last   int64    `json:"last"`
		Events []string `json:"events"`
	}{string(org), seq, prev, first, last, eventHashes})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Sealer seals an org's new events into signed blocks.
type Sealer struct {
	Store     *store.Store
	Keys      vault.KeyProvider
	Org       store.OrgID
	Interval  time.Duration // default 2s
	BatchSize int           // default 1000
	Logger    *slog.Logger
	Now       func() time.Time
}

func (s *Sealer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SealOnce seals what is waiting, a block at a time, and returns how many
// events were sealed.
func (s *Sealer) SealOnce(ctx context.Context) (int, error) {
	priv, keyID, err := SigningKey(s.Keys.Current(), s.Org)
	if err != nil {
		return 0, err
	}
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	batch := s.BatchSize
	if batch <= 0 {
		batch = 1000
	}
	total := 0
	for {
		n, err := s.Store.SealAudit(ctx, s.Org, batch, s.now(), func(prev store.AuditBlock, evs []store.ChainedEvent) (store.AuditBlock, error) {
			hashes := make([]string, len(evs))
			for i, e := range evs {
				hashes[i] = EventHash(s.Org, e)
			}
			h := BlockHash(s.Org, prev.Seq+1, evs[0].Seq, evs[len(evs)-1].Seq, prev.Hash, hashes)
			raw, _ := hex.DecodeString(h)
			return store.AuditBlock{Hash: h, KeyID: keyID, PublicKey: pub, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))}, nil
		})
		total += n
		if err != nil || n < batch {
			return total, err
		}
	}
}

// Run seals every Interval until ctx ends, and once more on the way out so a
// clean shutdown leaves nothing unsealed.
func (s *Sealer) Run(ctx context.Context) {
	every := s.Interval
	if every <= 0 {
		every = 2 * time.Second
	}
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if _, err := s.SealOnce(context.WithoutCancel(ctx)); err != nil {
				log.Error("audit: sealing on shutdown", "err", err)
			}
			return
		case <-t.C:
			if _, err := s.SealOnce(ctx); err != nil && ctx.Err() == nil {
				log.Error("audit: sealing", "err", err)
			}
		}
	}
}

// Report is the outcome of a verification.
type Report struct {
	OK         bool      `json:"ok"`
	Blocks     int       `json:"blocks"`
	Events     int       `json:"events"`
	Pruned     int       `json:"pruned_blocks"` // blocks whose events retention has deleted
	Unsealed   int       `json:"unsealed"`
	LastSealed time.Time `json:"last_sealed_at,omitzero"`
	Problem    string    `json:"problem,omitempty"`
	Keys       []string  `json:"key_ids"`
}

// Verify walks an org's chain and checks every block's link, hash and
// signature (against trusted keys only) and every remaining event. Blocks
// whose events retention has deleted keep their link checked; verification
// of events starts where events remain. Erased payloads keep their digest.
func Verify(ctx context.Context, st *store.Store, org store.OrgID, trusted TrustedKeys) (Report, error) {
	r := Report{}
	for id := range trusted {
		r.Keys = append(r.Keys, id)
	}
	blocks, err := st.AuditBlocks(ctx, org, 0, 0)
	if err != nil {
		return r, err
	}
	if r.Unsealed, err = st.CountUnsealed(ctx, org); err != nil {
		return r, err
	}
	fail := func(format string, args ...any) (Report, error) {
		r.Problem = fmt.Sprintf(format, args...)
		return r, nil
	}
	prevHash, prevLast := "", int64(0)
	for i, b := range blocks {
		r.Blocks++
		if b.Seq != int64(i+1) {
			return fail("block %d is missing (found %d)", i+1, b.Seq)
		}
		if b.PrevHash != prevHash || b.FirstSeq != prevLast+1 || b.LastSeq < b.FirstSeq {
			return fail("block %d does not follow block %d", b.Seq, b.Seq-1)
		}
		pub, ok := trusted[b.KeyID]
		if !ok || base64.StdEncoding.EncodeToString(pub) != b.PublicKey {
			return fail("block %d is signed with a key that is not trusted (%s)", b.Seq, b.KeyID)
		}
		sig, err1 := base64.StdEncoding.DecodeString(b.Signature)
		raw, err2 := hex.DecodeString(b.Hash)
		if err1 != nil || err2 != nil || !ed25519.Verify(pub, raw, sig) {
			return fail("block %d has a bad signature", b.Seq)
		}
		evs, err := st.AuditEventsInRange(ctx, org, b.FirstSeq, b.LastSeq)
		if err != nil {
			return r, err
		}
		switch {
		case len(evs) == 0 && r.Events == 0:
			// Retention deleted this block's events; nothing after a kept
			// event may be missing, which the next case catches.
			r.Pruned++
		case int64(len(evs)) != b.LastSeq-b.FirstSeq+1:
			return fail("block %d: %d of its %d events are missing", b.Seq, b.LastSeq-b.FirstSeq+1-int64(len(evs)), b.LastSeq-b.FirstSeq+1)
		default:
			hashes := make([]string, len(evs))
			for j, e := range evs {
				if e.Seq != b.FirstSeq+int64(j) {
					return fail("block %d: event %d is out of place", b.Seq, b.FirstSeq+int64(j))
				}
				if len(e.Payload) > 0 && store.PayloadDigest(e.Payload) != e.PayloadSHA256 {
					return fail("event %d: its recorded arguments were changed", e.Seq)
				}
				hashes[j] = EventHash(org, e)
			}
			if BlockHash(org, b.Seq, b.FirstSeq, b.LastSeq, b.PrevHash, hashes) != b.Hash {
				return fail("block %d: an event was changed", b.Seq)
			}
			r.Events += len(evs)
		}
		prevHash, prevLast, r.LastSealed = b.Hash, b.LastSeq, b.SealedAt
	}
	r.OK = true
	return r, nil
}
