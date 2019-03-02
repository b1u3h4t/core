package onchain

import (
	"context"
	"fmt"
	"math/big"

	//	"github.com/DOSNetwork/core/configuration"
	"github.com/DOSNetwork/core/share/vss/pedersen"
)

const (
	ETH = "ETH"
)

type ProxyAdapter interface {
	UploadID(ctx context.Context) (errc <-chan error)
	ResetNodeIDs(ctx context.Context) (errc <-chan error)
	RandomNumberTimeOut(ctx context.Context) (errc <-chan error)
	UploadPubKey(ctx context.Context, IdWithPubKeys chan [5]*big.Int) (errc chan error)
	SetRandomNum(ctx context.Context, signatures <-chan *vss.Signature) (errc chan error)
	DataReturn(ctx context.Context, signatures <-chan *vss.Signature) (errc chan error)
	Grouping(ctx context.Context, size int) <-chan error

	SubscribeEvent(subscribeType int, sink chan interface{}) chan error
	PollLogs(subscribeType int, sink chan interface{}) <-chan error

	LastUpdatedBlock() (blknum uint64, err error)
	GroupPubKey(idx int) (groupPubKeys [4]*big.Int, err error)

	Balance() (balance *big.Float)
	Address() (addr []byte)
	CurrentBlock() (blknum uint64, err error)
}

func NewProxyAdapter(ChainType, credentialPath, passphrase, proxyAddr string, urls []string) (ProxyAdapter, error) {
	switch ChainType {
	case ETH:
		adaptor, err := NewETHProxySession(credentialPath, passphrase, proxyAddr, urls)
		return adaptor, err
	default:
		err := fmt.Errorf("Chain %s not supported error\n", ChainType)
		return nil, err
	}
}
