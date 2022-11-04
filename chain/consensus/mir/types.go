package mir

import (
	"bytes"
	"fmt"

	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"github.com/multiformats/go-multihash"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/mir/pkg/checkpoint"
	"github.com/filecoin-project/mir/pkg/serializing"
	t "github.com/filecoin-project/mir/pkg/types"

	"github.com/filecoin-project/lotus/chain/types"
	ltypes "github.com/filecoin-project/lotus/chain/types"
)

const (
	// ConfigOffset is the number of epochs by which to delay configuration changes.
	// If a configuration is agreed upon in epoch e, it will take effect in epoch e + 1 + configOffset.
	ConfigOffset        = 2
	TransportType       = 0
	ReconfigurationType = 1
)

type Cfg struct {
	MembershipCfg interface{}
	DatastorePath string
}

func NewConfig(membership interface{}, datastore string) *Cfg {
	return &Cfg{membership, datastore}
}

var log = logging.Logger("mir-consensus")

// MirMessage interface that message types to be used in Mir need to implement.
type MirMessage interface {
	Serialize() ([]byte, error)
}

type MirMsgType int

const (
	ConfigMessageType = 0 // Mir specific config message
	SignedMessageType = 1 // Lotus signed message
)

func MsgType(m MirMessage) (MirMsgType, error) {
	switch m.(type) {
	case *types.SignedMessage:
		return SignedMessageType, nil
	default:
		return -1, fmt.Errorf("mir message type not implemented")

	}
}

func MessageBytes(msg MirMessage) ([]byte, error) {
	msgType, err := MsgType(msg)
	if err != nil {
		return nil, fmt.Errorf("unable to get msgType %w", err)
	}
	msgBytes, err := msg.Serialize()
	if err != nil {
		return nil, fmt.Errorf("unable to serialize message: %w", err)
	}
	return append(msgBytes, byte(msgType)), nil
}

// Mir's checkpoint period is computed as the number of validators times the SegmentLength.
// In order to configure the initial checkpoint period close to a specific value, we need
// to set the SegmentLength for the SMR system accordingly. This function does this math
// for you.
func segmentForCheckpointPeriod(desiredPeriod int, membership map[t.NodeID]t.NodeAddress) (int, error) {
	segment := desiredPeriod / len(membership)
	if segment < 1 {
		return 0, fmt.Errorf("wrong checkpoint period: the minimum checkpoint allowed for this number of validators is %d", len(membership))
	}
	return segment, nil
}

type ParentMeta struct {
	Height abi.ChainEpoch
	Cid    cid.Cid
}

type Checkpoint struct {
	// Height of the checkpoint
	Height abi.ChainEpoch
	// Cid of the blocks being committed in increasing order.
	// (index 0 is the first block of the range)
	BlockCids []cid.Cid
	// Parent checkpoint, i.e. metadata of previous checkpoint committed.
	Parent ParentMeta
}

func (ch *Checkpoint) isEmpty() bool {
	if ch.Height == 0 && ch.BlockCids == nil && (ch.Parent == ParentMeta{}) {
		return true
	}
	return false
}

func (ch *Checkpoint) Bytes() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := ch.MarshalCBOR(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (ch *Checkpoint) FromBytes(b []byte) error {
	return ch.UnmarshalCBOR(bytes.NewReader(b))
}

func (ch *Checkpoint) Cid() (cid.Cid, error) {
	if ch.isEmpty() {
		return cid.Undef, nil
	}
	b, err := ch.Bytes()
	if err != nil {
		return cid.Undef, err
	}

	h, err := multihash.Sum(b, abi.HashFunction, -1)
	if err != nil {
		return cid.Undef, err
	}

	return cid.NewCidV1(abi.CidBuilder.GetCodec(), h), nil
}

type CheckpointData struct {
	Checkpoint Checkpoint // checkpoint data
	Sn         uint64
	Config     EpochConfig // mir config information
}

type EpochConfig struct {
	EpochNr       uint64
	Memberships   []Membership
	Cert          map[string]CertSig // checkpoint certificate
	SegmentLength uint64
}

type CertSig struct {
	Sig []byte
}

// TODO: Use generics to deduplicate these methods
// for Checkpoint and CheckpointData.
func (ch *CheckpointData) Bytes() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := ch.MarshalCBOR(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (ch *CheckpointData) FromBytes(b []byte) error {
	return ch.UnmarshalCBOR(bytes.NewReader(b))
}

func NewCertSigFromPb(ch *checkpoint.StableCheckpoint) map[string]CertSig {
	cert := make(map[string]CertSig)
	for k, v := range ch.Cert {
		cert[k] = CertSig{v}
	}
	return cert
}

// FIXME: All this struct dance with membership is required due to Mir's protobuf
// definition and CBOR marshalling type limitations. We should definitely
// simplify this an make it more efficient.
type Membership struct {
	M map[string]MembershipInfo
}

type MembershipInfo struct {
	I string
}

func membershipToMapSlice(m []Membership) []map[string]string {
	out := make([]map[string]string, len(m))
	for k := range m {
		mmap := make(map[string]string)
		for mkey, mval := range m[k].M {
			mmap[mkey] = mval.I
		}
		out[k] = mmap
	}
	return out
}

func (m *Manager) NewEpochConfigFromPb(ch *checkpoint.StableCheckpoint) *EpochConfig {
	ms := make([]Membership, len(ch.Snapshot.Configuration.Memberships))
	for k, v := range ch.Snapshot.Configuration.Memberships {
		mmap := make(map[string]MembershipInfo)
		for mk, mv := range v.Membership {
			mmap[mk] = MembershipInfo{I: mv}
		}
		ms[k] = Membership{mmap}
	}
	return &EpochConfig{
		ch.Snapshot.Configuration.EpochNr,
		ms,
		NewCertSigFromPb(ch),
		uint64(m.segmentLength),
	}
}

// AsVRFProof serializes the data from CheckpointData
// that is included for a checkpoint inside a Filecoin block.
func (cd CheckpointData) AsVRFProof() (*ltypes.Ticket, error) {
	// we don´t include the certificate as it may be
	// different for each validator (leading blocks
	// with checkpoints from different validator to appear
	// completely different blocks).
	cd.Config.Cert = nil
	b, err := cd.Bytes()
	if err != nil {
		return nil, xerrors.Errorf("error serializing checkpoint data: %w", err)
	}
	return &ltypes.Ticket{VRFProof: b}, nil
}

func CheckpointFromVRFProof(t *ltypes.Ticket) (*CheckpointData, error) {
	ch := &CheckpointData{}
	err := ch.FromBytes(t.VRFProof)
	if err != nil {
		return nil, xerrors.Errorf("error getting checkpoint data from VRF Proof: %w", err)
	}
	return ch, nil
}

func (cd CheckpointData) ConfigAsElectionProof() (*ltypes.ElectionProof, error) {
	// deleting membership to reduce the size of the proof
	// we are only interested in the cert here.
	cd.Config.Memberships = nil
	buf := new(bytes.Buffer)
	if err := cd.Config.MarshalCBOR(buf); err != nil {
		return nil, xerrors.Errorf("error serializing checkpoint data: %w", err)
	}
	return &ltypes.ElectionProof{WinCount: 0, VRFProof: buf.Bytes()}, nil
}

func ConfigFromElectionProof(t *ltypes.ElectionProof) (*EpochConfig, error) {
	cfg := &EpochConfig{}
	if err := cfg.UnmarshalCBOR(bytes.NewReader(t.VRFProof)); err != nil {
		return nil, xerrors.Errorf("error getting checkpoint config from ElectionProof: %w", err)
	}
	return cfg, nil
}

func (cd *CheckpointData) Verify() error {
	membership := cd.Config.Memberships[0].M
	// Check if there is enough signatures.
	n := len(membership)
	f := (n - 1) / 3
	if len(cd.Config.Cert) < f+1 {
		return fmt.Errorf("not enough signatures in certificate: got %d, expected more than %d",
			len(cd.Config.Cert), f+1)
	}

	// Check whether all signatures are valid.
	checkBytes := serializing.CheckpointForSig(
		t.EpochNr(cd.Config.EpochNr),
		t.SeqNr(cd.Sn),
		hash(snapshotForHash(cd)))
	for nodeID, sig := range cd.Config.Cert {
		// For each signature in the certificate...

		// Check if the signing node is also in the given membership, thus "authorized" to sign.
		// TODO: Once nodes are identified by more than their ID
		//   (e.g., if a separate putlic key is part of their identity), adapt the check accordingly.
		if _, ok := membership[nodeID]; !ok {
			return fmt.Errorf("node %v not in membership", nodeID)
		}
		// Check if the signature is valid.
		if err := verifySig(checkBytes, sig.Sig, nodeID); err != nil {
			return fmt.Errorf("signature verification error (node %v): %w", nodeID, err)
		}
	}
	return nil
}