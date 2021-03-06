package dkg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/DOSNetwork/core/log"
	"github.com/DOSNetwork/core/p2p"
	"github.com/DOSNetwork/core/share"
	"github.com/DOSNetwork/core/share/vss/pedersen"
	"github.com/DOSNetwork/core/suites"

	"github.com/dedis/kyber"
)

const (
	CHANTIMEOUT    = 1   //IN SECOND
	REQUESTTIMEOUT = 600 //IN SECOND
)

type P2PDkgInterface interface {
	GetGroupPublicPoly(pubKeyCoor [4]*big.Int) *share.PubPoly
	GetSessionID(pubKeyCoor [4]*big.Int) (sessionId string)
	GetGroupIDs(pubKeyCoor [4]*big.Int) [][]byte
	GetGroupNumber() int
	GetAnyGroupIDs() [][]byte
	GetShareSecurity(pubKeyCoor [4]*big.Int) *share.PriShare
	GroupDismiss(pubKeyCoor [4]*big.Int) bool
	Start(ctx context.Context, groupIds [][]byte, sessionID string) (chan [5]*big.Int, <-chan error)
}

type P2PDkg struct {
	groupId     []byte
	suite       suites.Suite
	finSessions sync.Map
	outputChan  chan packageToLoop
	network     *p2p.P2PInterface
	logger      log.Logger
}

type DkgSession struct {
	SessionId      string
	GroupIds       [][]byte
	partSec        kyber.Scalar
	partPub        kyber.Point
	partDkg        *DistKeyGenerator
	pubKeys        Pubkeys
	pubkeyIdMap    map[string]string
	deals          []Deal
	partDks        *DistKeyShare
	groupPubPoly   *share.PubPoly
	subscribeEvent chan [5]*big.Int
	ctx            context.Context
	errc           chan error
	groupingStart  time.Time
}

func CreateP2PDkg(p p2p.P2PInterface, suite suites.Suite) (P2PDkgInterface, error) {
	d := &P2PDkg{
		groupId:    p.GetID(),
		suite:      suite,
		outputChan: make(chan packageToLoop),
		network:    &p,
		logger:     log.New("module", "dkg"),
	}
	return d, d.eventLoop()
}

func (d *P2PDkg) GetSessionID(pubKeyCoor [4]*big.Int) (sessionId string) {
	if targetSession, loaded := d.finSessions.Load(HashPoint(pubKeyCoor)); loaded {
		sessionId = targetSession.(*DkgSession).SessionId
	}
	return
}

func (d *P2PDkg) Start(ctx context.Context, groupIds [][]byte, sessionId string) (chan [5]*big.Int, <-chan error) {
	fmt.Println("P2PDkg Start")
	partSec := d.suite.Scalar().Pick(d.suite.RandomStream())
	newSession := &DkgSession{
		SessionId:      sessionId,
		GroupIds:       groupIds,
		partSec:        partSec,
		partPub:        d.suite.Point().Mul(partSec, nil),
		subscribeEvent: make(chan [5]*big.Int),
		ctx:            ctx,
		errc:           make(chan error),
	}
	d.logger.Event("DKGStart", nil)
	for i := 0; i < len(groupIds); i++ {
		start := time.Now()
		if !bytes.Equal((*d.network).GetID(), groupIds[i]) {
			retry := 0
			for {
				if retry >= 10 {
					break
				}
				if _, err := (*d.network).ConnectTo("", groupIds[i]); err != nil {
					fmt.Println("ConnectTo done retry=", retry, err)
					retry++
					time.Sleep(1 * time.Second)
				} else {
					break
				}
			}

			f := map[string]interface{}{
				"SessionID": sessionId,
				"retry":     retry,
				"costTime":  time.Since(start).Nanoseconds() / 1000,
				"From":      (*d.network).GetID(),
				"To":        groupIds[i]}
			if retry >= 10 {
				d.logger.Event("DKGConnectToFaile", f)
			} else {
				d.logger.Event("DKGConnectToSuccess", f)
			}
		}
	}
	go func() {
		var errcList []<-chan error
		outForDeal, pubKeyErrc := d.pipeExchangePubKey(newSession, d.outputChan)
		errcList = append(errcList, pubKeyErrc)
		outForResponse, genErrc := d.pipeNewDistKeyGenerator(outForDeal, d.outputChan)
		errcList = append(errcList, genErrc)
		RespErrc := d.pipeProcessDealAndResponses(outForResponse, d.outputChan)
		errcList = append(errcList, RespErrc)
		merge(newSession.errc, errcList...)
		newSession.groupingStart = time.Now()
	}()

	return newSession.subscribeEvent, newSession.errc
}

func (d *P2PDkg) GetGroupPublicPoly(pubKeyCoor [4]*big.Int) *share.PubPoly {
	if targetSession, loaded := d.finSessions.Load(HashPoint(pubKeyCoor)); loaded {
		return targetSession.(*DkgSession).groupPubPoly
	}
	return nil
}

func (d *P2PDkg) GetGroupNumber() int {
	length := 0
	d.finSessions.Range(func(_, _ interface{}) bool {
		length++
		return true
	})

	return length
}

func (d *P2PDkg) GetAnyGroupIDs() [][]byte {
	var ids [][]byte
	d.finSessions.Range(func(_, targetSession interface{}) bool {
		ids = make([][]byte, len(targetSession.(*DkgSession).GroupIds))
		copy(ids, targetSession.(*DkgSession).GroupIds)
		return false
	})
	return ids
}

func (d *P2PDkg) GetGroupIDs(pubKeyCoor [4]*big.Int) [][]byte {
	if targetSession, loaded := d.finSessions.Load(HashPoint(pubKeyCoor)); loaded {
		return targetSession.(*DkgSession).GroupIds
	}
	return nil
}

func (d *P2PDkg) GetShareSecurity(pubKeyCoor [4]*big.Int) *share.PriShare {
	if targetSession, loaded := d.finSessions.Load(HashPoint(pubKeyCoor)); loaded {
		return targetSession.(*DkgSession).partDks.Share
	}
	return nil
}

func (d *P2PDkg) GroupDismiss(pubKeyCoor [4]*big.Int) bool {
	d.finSessions.Delete(HashPoint(pubKeyCoor))
	length := 0
	d.finSessions.Range(func(_, _ interface{}) bool {
		length++
		return true
	})

	d.logger.TimeTrack(time.Now(), "dkgDismiss", map[string]interface{}{"size": length})

	return length == 0
}

type packageToLoop struct {
	sessionId string
	content   interface{}
}

type packageToPeer struct {
	pubKey *vss.PublicKey
	deals  map[string]*Deal
	resps  *Responses
}

func (d *P2PDkg) eventLoop() (err error) {
	peerEvent, err := (*d.network).SubscribeEvent(100, ReqPublicKey{}, ReqDeal{}, ReqResponses{})
	if err == nil {
		go func() {
			defer (*d.network).UnSubscribeEvent(ReqPublicKey{}, ReqDeal{}, ReqResponses{})
			peerPackageMap := make(map[string]packageToPeer)
			for {
				select {
				case msg := <-peerEvent:
					//fmt.Println("DKG peerEvent")

					switch content := msg.Msg.Message.(type) {
					case *ReqPublicKey:
						//fmt.Println("DKG ReqPublicKey")

						var pubKey *vss.PublicKey
						if peerPackageMap[content.SessionId].pubKey == nil {
							//fmt.Println("DKG pubKey is nil")
							pubKey = &vss.PublicKey{}
						} else {
							pubKey = peerPackageMap[content.SessionId].pubKey
						}
						(*d.network).Reply(msg.Sender, msg.RequestNonce, pubKey)
					case *ReqDeal:
						var deal *Deal
						//fmt.Println("DKG ReqDeal")

						if peerPackageMap[content.SessionId].deals[string(msg.Sender)] == nil {
							deal = &Deal{}
						} else {
							deal = peerPackageMap[content.SessionId].deals[string(msg.Sender)]
						}
						(*d.network).Reply(msg.Sender, msg.RequestNonce, deal)
					case *ReqResponses:
						var resp *Responses
						//fmt.Println("DKG ReqResponses")

						if peerPackageMap[content.SessionId].resps == nil {
							//fmt.Println("DKG resps is nil")
							resp = &Responses{}
						} else {
							resp = peerPackageMap[content.SessionId].resps
						}
						(*d.network).Reply(msg.Sender, msg.RequestNonce, resp)
					default:
						//fmt.Println("DKG unknown request type")
					}
				case msg := <-d.outputChan:
					switch content := msg.content.(type) {
					case *DkgSession:
						pubKey := content.groupPubPoly.Commit()
						pubKeyCoor, err := decodePubKey(pubKey)
						if err != nil {
							content.errc <- errors.New("dkg: decode share public key fail")
							continue
						}
						if _, loaded := d.finSessions.LoadOrStore(HashPoint(pubKeyCoor), content); loaded {
							content.errc <- errors.New("dkg: duplicate share public key")
							continue
						}
						timeCost := time.Since(content.groupingStart).Seconds()
						fmt.Println("DistKeyShare SUCCESS ", timeCost)

						length := 0
						d.finSessions.Range(func(_, _ interface{}) bool {
							length++
							return true
						})
						d.logger.TimeTrack(time.Now(), "dkgSucc", map[string]interface{}{"size": length})

						groupId, _ := new(big.Int).SetString(content.SessionId, 16)
						dataReturn := [5]*big.Int{groupId}
						copy(dataReturn[1:], pubKeyCoor[:])
						if content.subscribeEvent != nil {
							content.subscribeEvent <- dataReturn
						}
					case vss.PublicKey:
						peerPackageMap[msg.sessionId] = packageToPeer{pubKey: &content}
					case map[string]*Deal:
						pack, _ := peerPackageMap[msg.sessionId]
						pack.deals = content
						peerPackageMap[msg.sessionId] = pack
					case Responses:
						pack, _ := peerPackageMap[msg.sessionId]
						pack.resps = &content
						peerPackageMap[msg.sessionId] = pack
					default:
						fmt.Println("unknown output type")
					}
				}
			}
		}()
	}
	return
}

func merge(out chan error, cs ...<-chan error) <-chan error {
	var wg sync.WaitGroup
	output := func(c <-chan error) {
		for n := range c {
			timer := time.NewTimer(CHANTIMEOUT * time.Second)
			select {
			case out <- n:
			case <-timer.C:
			}
		}
		wg.Done()
	}
	wg.Add(len(cs))
	for _, c := range cs {
		go output(c)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

func decodePubKey(pubKey kyber.Point) (pubKeyCoor [4]*big.Int, err error) {
	pubKeyMar, err := pubKey.MarshalBinary()
	if err != nil {
		return
	}

	for i := 0; i < 4; i++ {
		pubKeyCoor[i] = new(big.Int).SetBytes(pubKeyMar[32*i+1 : 32*i+33])
	}

	return
}

func HashPoint(pubKeyCoor [4]*big.Int) string {
	var pointBytes []byte
	for _, coor := range pubKeyCoor {
		pointBytes = append(pointBytes, coor.Bytes()...)
	}
	pubKeyhash := sha256.Sum256(pointBytes)
	return string(pubKeyhash[:])
}

func (d *P2PDkg) pipeExchangePubKey(newSession *DkgSession, outToEventloop chan<- packageToLoop) (<-chan *DkgSession, <-chan error) {
	out := make(chan *DkgSession)
	errc := make(chan error)
	go func() {
		defer d.logger.TimeTrack(time.Now(), "pipeExchangePubKey", map[string]interface{}{"SessionID": newSession.ctx.Value("SessionID")})

		defer close(errc)
		defer close(out)
		d.logger.Event("pipeExchangePubKey", nil)
		newSession.pubkeyIdMap = make(map[string]string)
		newSession.pubkeyIdMap[newSession.partPub.String()] = string(d.groupId)
		newSession.pubKeys = append(newSession.pubKeys, newSession.partPub)
		public := vss.PublicKey{SenderId: d.groupId}
		if err := public.SetPoint(d.suite, newSession.partPub); err != nil {
			d.logger.Error(err)
			select {
			case errc <- err:
			case <-newSession.ctx.Done():
			}
			fmt.Println("pipeExchangePubKey closed")
			return
		}
		select {
		case outToEventloop <- packageToLoop{sessionId: newSession.SessionId, content: public}:
		case <-newSession.ctx.Done():
			fmt.Println("pipeExchangePubKey closed")
			return
		}

		groupPeers := make(map[string][]byte)
		for _, id := range newSession.GroupIds {
			if bytes.Compare(id, d.groupId) != 0 {
				groupPeers[string(id)] = id
			}
		}
		timer := time.NewTimer(REQUESTTIMEOUT * time.Second)
		for len(groupPeers) > 0 {
			for key, id := range groupPeers {
				select {
				case <-newSession.ctx.Done():
					fmt.Println("pipeExchangePubKey closed")
					return
				default:
				}
				pubkey, err := (*d.network).Request(id, &ReqPublicKey{SessionId: newSession.SessionId})
				if err != nil {
					select {
					case <-timer.C:
						errc <- errors.New("pipeExchangePubKey request timeout")
						return
					case <-newSession.ctx.Done():
						fmt.Println("pipeExchangePubKey closed")
						return
					default:
						continue
					}
				}
				switch content := pubkey.Msg.Message.(type) {
				case *vss.PublicKey:
					if content.GetBinary() != nil {
						p, err := content.GetPoint(d.suite)
						if err != nil {
							d.logger.Error(err)
							select {
							case errc <- err:
							case <-newSession.ctx.Done():
							}
							fmt.Println("pipeExchangePubKey closed")
							return
						}
						newSession.pubkeyIdMap[p.String()] = string(content.SenderId)
						newSession.pubKeys = append(newSession.pubKeys, p)
						delete(groupPeers, key)
					}
				default:
				}
			}
		}
		select {
		case out <- newSession:
		case <-newSession.ctx.Done():
			fmt.Println("pipeExchangePubKey closed")
			return
		}
		fmt.Println("pipeExchangePubKey closed")
	}()

	return out, errc
}

func (d *P2PDkg) pipeNewDistKeyGenerator(dkgSession <-chan *DkgSession, outToEventloop chan<- packageToLoop) (<-chan *DkgSession, <-chan error) {
	out := make(chan *DkgSession)
	errc := make(chan error)

	go func() {
		defer close(errc)
		defer close(out)
		for newSession := range dkgSession {
			defer d.logger.TimeTrack(time.Now(), "pipeNewDistKeyGenerator", map[string]interface{}{"SessionID": newSession.ctx.Value("SessionID")})
			var err error
			sort.Sort(newSession.pubKeys)
			newSession.partDkg, err = NewDistKeyGenerator(d.suite, newSession.partSec, newSession.pubKeys, len(newSession.GroupIds)/2+1)
			if err != nil {
				d.logger.Error(err)
				select {
				case errc <- err:
				case <-newSession.ctx.Done():
				}
				fmt.Println("pipeNewDistKeyGenerator closed")
				return
			}

			idxDealMap, err := newSession.partDkg.Deals()
			if err != nil {
				d.logger.Error(err)
				select {
				case errc <- err:
				case <-newSession.ctx.Done():
				}
				fmt.Println("pipeNewDistKeyGenerator closed")
				return
			}

			idDealMap := make(map[string]*Deal)
			for i, pub := range newSession.pubKeys {
				idDealMap[newSession.pubkeyIdMap[pub.String()]] = idxDealMap[i]
			}
			select {
			case outToEventloop <- packageToLoop{sessionId: newSession.SessionId, content: idDealMap}:
			case <-newSession.ctx.Done():
				fmt.Println("pipeExchangePubKey closed")
				return
			}

			groupPeers := make(map[string][]byte)
			for _, id := range newSession.GroupIds {
				if bytes.Compare(id, d.groupId) != 0 {
					groupPeers[string(id)] = id
				}
			}
			timer := time.NewTimer(REQUESTTIMEOUT * time.Second)
			for len(groupPeers) > 0 {
				for key, id := range groupPeers {
					select {
					case <-newSession.ctx.Done():
						fmt.Println("pipeNewDistKeyGenerator closed")
						return
					default:
					}
					deal, err := (*d.network).Request(id, &ReqDeal{SessionId: newSession.SessionId})
					if err != nil {
						select {
						case <-timer.C:
							errc <- errors.New("pipeNewDistKeyGenerator request timeout")
							return
						case <-newSession.ctx.Done():
							fmt.Println("pipeNewDistKeyGenerator closed")
							return
						default:
							continue
						}
					}
					switch content := deal.Msg.Message.(type) {
					case *Deal:
						if content.GetDeal() != nil {
							newSession.deals = append(newSession.deals, *content)
							delete(groupPeers, key)
						}
					default:
					}
				}
			}
			select {
			case out <- newSession:
			case <-newSession.ctx.Done():
				fmt.Println("pipeNewDistKeyGenerator closed")
				return
			}
		}
		fmt.Println("pipeNewDistKeyGenerator closed")
	}()

	return out, errc
}

func (d *P2PDkg) pipeProcessDealAndResponses(dkgSession <-chan *DkgSession, outToEventloop chan<- packageToLoop) <-chan error {
	errc := make(chan error)

	go func() {
		defer close(errc)
		for newSession := range dkgSession {
			defer d.logger.TimeTrack(time.Now(), "pipeProcessDealAndResponses", map[string]interface{}{"SessionID": newSession.ctx.Value("SessionID")})

			var resps []*Response
			for _, deal := range newSession.deals {
				resp, err := (*newSession.partDkg).ProcessDeal(&deal)
				if err != nil {
					d.logger.Error(err)
					select {
					case errc <- err:
					case <-newSession.ctx.Done():
					}
					fmt.Println("pipeProcessDealAndResponses closed")
					return
				} else {
					resps = append(resps, resp)
				}
			}
			select {
			case outToEventloop <- packageToLoop{sessionId: newSession.SessionId, content: Responses{Response: resps}}:
			case <-newSession.ctx.Done():
				fmt.Println("pipeExchangePubKey closed")
				return
			}

			groupPeers := make(map[string][]byte)
			for _, id := range newSession.GroupIds {
				if bytes.Compare(id, d.groupId) != 0 {
					groupPeers[string(id)] = id
				}
			}
			timer := time.NewTimer(REQUESTTIMEOUT * time.Second)
			for len(groupPeers) > 0 {
				for key, id := range groupPeers {
					select {
					case <-newSession.ctx.Done():
						fmt.Println("pipeNewDistKeyGenerator closed")
						return
					default:
					}
					responses, err := (*d.network).Request(id, &ReqResponses{SessionId: newSession.SessionId})
					if err != nil {
						select {
						case <-timer.C:
							errc <- errors.New("pipeProcessDealAndResponses request timeout")
							return
						case <-newSession.ctx.Done():
							fmt.Println("pipeProcessDealAndResponses closed")
							return
						default:
							continue
						}
					}
					switch content := responses.Msg.Message.(type) {
					case *Responses:
						if resps := content.GetResponse(); resps != nil {
							for _, r := range resps {
								if _, err := (*newSession.partDkg).ProcessResponse(r); err != nil {
									d.logger.Error(err)
									select {
									case errc <- err:
									case <-newSession.ctx.Done():
									}
									fmt.Println("pipeProcessDealAndResponses closed")
									return
								}
							}
							delete(groupPeers, key)
						}
					default:
					}
				}
			}
			if (*newSession.partDkg).Certified() {
				var err error
				newSession.partDks, err = newSession.partDkg.DistKeyShare()
				if err != nil {
					d.logger.Error(err)
					select {
					case errc <- err:
					case <-newSession.ctx.Done():
					}
					fmt.Println("pipeProcessDealAndResponses closed")
					return
				}
				newSession.groupPubPoly = share.NewPubPoly(d.suite, d.suite.Point().Base(), newSession.partDks.Commitments())
				select {
				case outToEventloop <- packageToLoop{content: newSession}:
				case <-newSession.ctx.Done():
					fmt.Println("pipeNewDistKeyGenerator closed")
					return
				}
			} else {
				select {
				case errc <- errors.New("partDkg not certified"):
				case <-newSession.ctx.Done():
				}
				fmt.Println("pipeProcessDealAndResponses closed")
				return
			}
		}
		fmt.Println("pipeProcessDealAndResponses closed")
	}()

	return errc
}
