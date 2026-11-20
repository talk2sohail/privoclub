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
	"maps"
	"slices"
	"strconv"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// The tamper-evident chain (ADR 0006, 0028): a sealer puts new events into
// blocks, each covering the previous one, and signs each block with a key
// derived from the master key, which never touches the database.

const signingSalt = "privo audit signing v1"

// SettingRetentionDays is how many days sealed events are kept; "0" or
// unset keeps them for ever.
const SettingRetentionDays = "audit.retention_days"

// MaxRetentionDays bounds the setting (ten years).
const MaxRetentionDays = 3650

// MinRetentionDays is the least retention keeps, whatever the setting says
// (security review of F12): a mistyped setting cannot empty the trail.
const MinRetentionDays = 30

// backdateGrace is how far before the previous block's seal an event may be
// dated: events are written as they happen, and a completion waits in a queue
// for at most seconds.
const backdateGrace = 5 * time.Minute

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
	UserID     string `json:"user_id"`
	Outcome    string `json:"outcome"`
	LatencyMS  int64  `json:"latency_ms"`
	Payload    string `json:"payload_sha256"`
}

// EventHash is the hex SHA-256 of an event's chained fields.
func EventHash(org store.OrgID, e store.ChainedEvent) string {
	b, _ := json.Marshal(chainedFields{Org: string(org), Seq: e.Seq, ID: e.ID, CallID: e.CallID, Phase: e.Phase,
		Time: e.Time.UTC().Format(time.RFC3339Nano), Actor: e.Actor, ActorID: e.ActorID, Client: e.Client, Server: e.Server, Item: e.Item,
		Action: e.Action, Decision: e.Decision, ApprovedBy: e.ApprovedBy, UserID: e.UserID, Outcome: e.Outcome, LatencyMS: e.LatencyMS,
		Payload: e.PayloadSHA256})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BlockHash is the hex SHA-256 of a block: the org, its number, the previous
// block's hash, its range, when it was sealed and its events' hashes in
// order. The seal time is covered so that it can bound how old the next
// block's events may claim to be.
func BlockHash(org store.OrgID, seq, first, last int64, prev string, sealedAt time.Time, eventHashes []string) string {
	b, _ := json.Marshal(struct {
		Org    string   `json:"org"`
		Seq    int64    `json:"seq"`
		Prev   string   `json:"prev"`
		First  int64    `json:"first"`
		Last   int64    `json:"last"`
		Sealed string   `json:"sealed_at"`
		Events []string `json:"events"`
	}{string(org), seq, prev, first, last, sealedAt.UTC().Format(time.RFC3339Nano), eventHashes})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// pruneHash is what a prune record's signature covers: that the org's events
// up to through may be gone, as its record number seq.
func pruneHash(org store.OrgID, seq, through int64) []byte {
	b, _ := json.Marshal(struct {
		Kind    string `json:"kind"`
		Org     string `json:"org"`
		Seq     int64  `json:"seq"`
		Through int64  `json:"through_seq"`
	}{"prune", string(org), seq, through})
	sum := sha256.Sum256(b)
	return sum[:]
}

// endorseHash is what an endorsement's signature covers: that the key with
// this id and public key signed for the org.
func endorseHash(org store.OrgID, keyID, pub string) []byte {
	b, _ := json.Marshal(struct {
		Kind   string `json:"kind"`
		Org    string `json:"org"`
		KeyID  string `json:"key_id"`
		Public string `json:"public_key"`
	}{"endorse", string(org), keyID, pub})
	sum := sha256.Sum256(b)
	return sum[:]
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
		at := s.now()
		n, err := s.Store.SealAudit(ctx, s.Org, batch, at, func(prev store.AuditBlock, evs []store.ChainedEvent) (store.AuditBlock, error) {
			hashes := make([]string, len(evs))
			for i, e := range evs {
				hashes[i] = EventHash(s.Org, e)
			}
			h := BlockHash(s.Org, prev.Seq+1, evs[0].Seq, evs[len(evs)-1].Seq, prev.Hash, at, hashes)
			raw, _ := hex.DecodeString(h)
			return store.AuditBlock{Hash: h, KeyID: keyID, PublicKey: pub, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))}, nil
		})
		total += n
		if err != nil || n < batch {
			return total, err
		}
	}
}

// Prune applies the org's retention: the events of blocks sealed more than
// that many days ago (at least MinRetentionDays) are deleted, whole blocks
// at a time, under a signed prune record. It returns how many events went.
func (s *Sealer) Prune(ctx context.Context) (int, error) {
	v, err := s.Store.Setting(ctx, s.Org, SettingRetentionDays, "0")
	if err != nil {
		return 0, err
	}
	days, err := strconv.Atoi(v)
	if err != nil {
		// Nothing is deleted on a setting that cannot be read, and it is said.
		return 0, fmt.Errorf("audit: retention %q is not a number of days", v)
	}
	if days <= 0 {
		return 0, nil
	}
	return s.pruneBefore(ctx, s.now().AddDate(0, 0, -min(max(days, MinRetentionDays), MaxRetentionDays)))
}

// pruneBefore deletes the events of blocks sealed before before, and signs
// the record that allows it (security review of F12: deletion is retention
// only when a trusted key says so).
func (s *Sealer) pruneBefore(ctx context.Context, before time.Time) (int, error) {
	priv, keyID, err := SigningKey(s.Keys.Current(), s.Org)
	if err != nil {
		return 0, err
	}
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	return s.Store.PruneAudit(ctx, s.Org, before, s.now(), func(seq, through int64) (store.AuditPrune, error) {
		sig := ed25519.Sign(priv, pruneHash(s.Org, seq, through))
		return store.AuditPrune{KeyID: keyID, PublicKey: pub, Signature: base64.StdEncoding.EncodeToString(sig)}, nil
	})
}

// Endorse records, during a master key rotation, that the current key vouches
// for the previous one, so blocks the previous key signed keep verifying once
// it is gone. Without a previous key it does nothing.
func (s *Sealer) Endorse(ctx context.Context) error {
	if len(s.Keys.Previous()) == 0 {
		return nil
	}
	cur, curID, err := SigningKey(s.Keys.Current(), s.Org)
	if err != nil {
		return err
	}
	old, oldID, err := SigningKey(s.Keys.Previous(), s.Org)
	if err != nil {
		return err
	}
	if oldID == curID {
		return nil
	}
	pub := base64.StdEncoding.EncodeToString(old.Public().(ed25519.PublicKey))
	sig := ed25519.Sign(cur, endorseHash(s.Org, oldID, pub))
	return s.Store.AddAuditKeyEndorsement(ctx, s.Org, store.AuditKeyEndorsement{KeyID: oldID, PublicKey: pub, EndorsedBy: curID,
		Signature: base64.StdEncoding.EncodeToString(sig), EndorsedAt: s.now()})
}

// Run seals every Interval until ctx ends, and once more on the way out so a
// clean shutdown leaves nothing unsealed. It applies retention at start and
// every hour.
func (s *Sealer) Run(ctx context.Context) {
	every := s.Interval
	if every <= 0 {
		every = 2 * time.Second
	}
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	// During a master key rotation, the new key vouches for the old one.
	if err := s.Endorse(ctx); err != nil && ctx.Err() == nil {
		log.Error("audit: endorse the previous signing key", "err", err)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	prune := func() {
		if n, err := s.Prune(ctx); err != nil && ctx.Err() == nil {
			log.Error("audit: retention", "err", err)
		} else if n > 0 {
			log.Info("audit: retention deleted old events", "events", n)
		}
	}
	prune()
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	for {
		select {
		case <-hourly.C:
			prune()
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

// Verify walks an org's chain and checks every block's link, hash, seal time
// and signature (against trusted keys, and keys a trusted key endorsed) and
// every remaining event. Events may be missing only where a signed prune
// record allows it; no sealed event may sit outside the blocks, and none may
// be dated before the previous block was sealed. Erased payloads keep their
// digest. What verification cannot see is a rewrite of the newest blocks by
// someone holding the master key, or the newest blocks dropped whole: only
// checkpoints kept outside Privo show that.
func Verify(ctx context.Context, st *store.Store, org store.OrgID, trusted TrustedKeys) (Report, error) {
	r := Report{}
	fail := func(format string, args ...any) (Report, error) {
		r.Problem = fmt.Sprintf(format, args...)
		return r, nil
	}
	trusted, err := withEndorsed(ctx, st, org, trusted)
	if err != nil {
		return r, err
	}
	for id := range trusted {
		r.Keys = append(r.Keys, id)
	}
	slices.Sort(r.Keys)
	trustedSig := func(keyID, pub, sig string, msg []byte) (known, good bool) {
		k, ok := trusted[keyID]
		if !ok || base64.StdEncoding.EncodeToString(k) != pub {
			return false, false
		}
		raw, err := base64.StdEncoding.DecodeString(sig)
		return true, err == nil && ed25519.Verify(k, msg, raw)
	}

	// Retention: how far events may be gone.
	prunes, err := st.AuditPrunes(ctx, org)
	if err != nil {
		return r, err
	}
	var gone int64
	for i, p := range prunes {
		if p.Seq != int64(i+1) || p.ThroughSeq < gone {
			return fail("prune record %d is out of order", i+1)
		}
		known, good := trustedSig(p.KeyID, p.PublicKey, p.Signature, pruneHash(org, p.Seq, p.ThroughSeq))
		if !known {
			return fail("prune record %d is signed with a key that is not trusted (%s)", p.Seq, p.KeyID)
		}
		if !good {
			return fail("prune record %d has a bad signature", p.Seq)
		}
		gone = p.ThroughSeq
	}

	blocks, err := st.AuditBlocks(ctx, org, 0, 0)
	if err != nil {
		return r, err
	}
	if r.Unsealed, err = st.CountUnsealed(ctx, org); err != nil {
		return r, err
	}
	prevHash, prevLast := "", int64(0)
	var prevSealed time.Time
	for i, b := range blocks {
		r.Blocks++
		if b.Seq != int64(i+1) {
			return fail("block %d is missing (found %d)", i+1, b.Seq)
		}
		if b.PrevHash != prevHash || b.FirstSeq != prevLast+1 || b.LastSeq < b.FirstSeq {
			return fail("block %d does not follow block %d", b.Seq, b.Seq-1)
		}
		if i > 0 && b.SealedAt.Before(prevSealed) {
			return fail("block %d was sealed before block %d", b.Seq, b.Seq-1)
		}
		raw, err := hex.DecodeString(b.Hash)
		if err != nil {
			return fail("block %d has a bad hash", b.Seq)
		}
		known, good := trustedSig(b.KeyID, b.PublicKey, b.Signature, raw)
		if !known {
			return fail("block %d is signed with a key that is not trusted (%s)", b.Seq, b.KeyID)
		}
		if !good {
			return fail("block %d has a bad signature", b.Seq)
		}
		evs, err := st.AuditEventsInRange(ctx, org, b.FirstSeq, b.LastSeq)
		if err != nil {
			return r, err
		}
		switch {
		case len(evs) == 0 && r.Events == 0 && b.LastSeq <= gone:
			// Retention deleted this block's events under a signed record.
			// The block's hash cannot be recomputed without them, but its
			// link and signature were checked above.
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
				if i > 0 && e.Time.Before(prevSealed.Add(-backdateGrace)) {
					return fail("event %d is dated before block %d was sealed: it was added later and backdated", e.Seq, b.Seq-1)
				}
				hashes[j] = EventHash(org, e)
			}
			if BlockHash(org, b.Seq, b.FirstSeq, b.LastSeq, b.PrevHash, b.SealedAt, hashes) != b.Hash {
				return fail("block %d: an event or its seal time was changed", b.Seq)
			}
			r.Events += len(evs)
		}
		prevHash, prevLast, prevSealed, r.LastSealed = b.Hash, b.LastSeq, b.SealedAt, b.SealedAt
	}
	if gone > prevLast {
		return fail("a prune record reaches past the last block")
	}
	// An event that claims a place no block covers would never be checked,
	// yet search and exports would show it.
	if n, err := st.CountSealedOutside(ctx, org, prevLast); err != nil {
		return r, err
	} else if n > 0 {
		return fail("%d events claim a place outside the sealed blocks", n)
	}
	r.OK = true
	return r, nil
}

// withEndorsed adds to trusted every key that a trusted key endorsed, and so
// on, so blocks signed before a master key rotation keep verifying.
func withEndorsed(ctx context.Context, st *store.Store, org store.OrgID, trusted TrustedKeys) (TrustedKeys, error) {
	es, err := st.AuditKeyEndorsements(ctx, org)
	if err != nil {
		return nil, err
	}
	out := TrustedKeys{}
	maps.Copy(out, trusted)
	for added := true; added; {
		added = false
		for _, e := range es {
			if _, ok := out[e.KeyID]; ok {
				continue
			}
			by, ok := out[e.EndorsedBy]
			if !ok {
				continue
			}
			pub, err1 := base64.StdEncoding.DecodeString(e.PublicKey)
			sig, err2 := base64.StdEncoding.DecodeString(e.Signature)
			if err1 != nil || err2 != nil || len(pub) != ed25519.PublicKeySize || KeyID(pub) != e.KeyID || !ed25519.Verify(by, endorseHash(org, e.KeyID, e.PublicKey), sig) {
				continue
			}
			out[e.KeyID] = ed25519.PublicKey(pub)
			added = true
		}
	}
	return out, nil
}
