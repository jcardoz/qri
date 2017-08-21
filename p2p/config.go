package p2p

import (
	"crypto/rand"
	"fmt"
	"github.com/qri-io/qri/repo"

	crypto "github.com/libp2p/go-libp2p-crypto"
	peer "github.com/libp2p/go-libp2p-peer"
	ma "github.com/multiformats/go-multiaddr"
	fs_repo "github.com/qri-io/qri/repo/fs"
)

// NodeCfg is all configuration options for a Qri Node
type NodeCfg struct {
	PeerId peer.ID // peer identifier

	PubKey  crypto.PubKey
	PrivKey crypto.PrivKey

	// Bring-Your-Own Qri Repo...
	Repo repo.Repo
	// Or supply a filepath to one
	RepoPath string

	// default port to bind tcp listener to
	// ignored if Addrs is supplied
	Port int
	// List of multiaddresses to listen on
	Addrs []ma.Multiaddr
	// secure connection flag. if true
	// PubKey & PrivKey are required
	Secure bool
}

// DefaultNodeCfg generates sensible settings for a Qri Node
func DefaultNodeCfg() *NodeCfg {
	r := rand.Reader

	// Generate a key pair for this host. We will use it at least
	// to obtain a valid host ID.
	priv, pub, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
	if err != nil {
		return nil
	}

	// Obtain Peer ID from public key
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		return nil
	}

	return &NodeCfg{
		PeerId:   pid,
		PrivKey:  priv,
		PubKey:   pub,
		RepoPath: "~/qri",
		// TODO - enabling this causes all nodes to broadcast
		// on the same address, which isn't good. figure out why
		// Port:     4444,
		Secure: true,
	}
}

// Validate confirms that the given settings will work, returning an error if not.
func (cfg *NodeCfg) Validate() error {

	if cfg.Repo == nil && cfg.RepoPath != "" {
		repo, err := fs_repo.NewRepo(cfg.RepoPath)
		if err != nil {
			return err
		}
		cfg.Repo = repo
	}

	// If no listening addresses are set, allocate
	// a tcp multiaddress on local host bound to the default port
	if cfg.Addrs == nil {
		// find an open tcp port
		port, err := LocalOpenPort("tcp", cfg.Port)
		if err != nil {
			return err
		}

		// Create a multiaddress
		addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port))
		if err != nil {
			return err
		}
		cfg.Addrs = []ma.Multiaddr{addr}
	}

	if cfg.Secure && cfg.PubKey == nil {
		return fmt.Errorf("NodeCfg error: PubKey is required for Secure communication")
	} else if cfg.Secure && cfg.PrivKey == nil {
		return fmt.Errorf("NodeCfg error: PrivKey is required for Secure communication")
	}

	// TODO - more checks
	return nil
}
