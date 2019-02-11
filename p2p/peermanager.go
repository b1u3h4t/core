package p2p

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/DOSNetwork/core/log"
	"github.com/DOSNetwork/core/p2p/dht"
	"time"
)

var MAXPEERCONNCOUNT uint32 = 100000

func init() {
	temp, err := strconv.ParseUint(os.Getenv("MAXPEERCONNCOUNT"), 10, 32)
	if err == nil {
		MAXPEERCONNCOUNT = uint32(temp)
		fmt.Println("MAXPEERCONNCOUNT", MAXPEERCONNCOUNT)
	}
}

type PeerConnManager struct {
	mu        sync.RWMutex
	timeFrame time.Duration
	peers     map[string]*PeerConn
	logger    log.Logger
}

func CreatePeerConnManager() (pconn *PeerConnManager) {
	pconn = &PeerConnManager{
		timeFrame: -time.Second,
		peers:     make(map[string]*PeerConn),
		logger:    log.New("module", "ConnManager"),
	}
	return pconn
}

func (pm *PeerConnManager) SetTimeFrame(d time.Duration) {
	pm.timeFrame = d
}

func (pm *PeerConnManager) FindLessUsedPeerConn() (pconn *PeerConn) {
	var lastusedtime int64
	lastusedtime = time.Now().Add(-pm.timeFrame).Unix()
	pconn = nil
	for _, value := range pm.peers {
		if value.lastusedtime.Unix() < lastusedtime {
			lastusedtime = value.lastusedtime.Unix()
			pconn = value
		}
	}
	return
}

func (pm *PeerConnManager) LoadOrStore(id string, peer *PeerConn) (actual *PeerConn, loaded bool) {
	pm.logger.Event("LoadOrStore")
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if actual, loaded = pm.peers[id]; loaded {
		if peer.incomingConn == actual.incomingConn {
			peer.EndWithoutDelete()
		} else {
			if actual.incomingConn == !dht.Less(peer.identity, peer.p2pnet.identity) {
				peer.EndWithoutDelete()
			} else {
				pm.logger.Event("PMReplaceNewPeer")
				delete(pm.peers, id)
				actual.EndWithoutDelete()
				pm.peers[id] = peer
				actual = peer
				loaded = false
			}
		}
		return
	}
	pm.logger.Event("PMInsertNewPeer")
	loaded = false
	if uint32(len(pm.peers)) >= MAXPEERCONNCOUNT {
		p := pm.FindLessUsedPeerConn()
		if p != nil {
			p.EndWithoutDelete()
			delete(pm.peers, string(p.identity.Id))
			pm.peers[id] = peer
			actual = peer
		} else {
			actual = nil
		}
	} else {
		pm.peers[id] = peer
		actual = peer
	}
	return
}

func (pm *PeerConnManager) Range(f func(key, value interface{}) bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for key, value := range pm.peers {
		if !f(key, value) {
			break
		}
	}
}

func (pm *PeerConnManager) GetPeerByID(id string) *PeerConn {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	value, ok := pm.peers[id]
	if ok {
		return value
	} else {
		return nil
	}
}

func (pm *PeerConnManager) DeletePeer(id string) {
	if pm.GetPeerByID(id) != nil {
		pm.mu.Lock()
		defer pm.mu.Unlock()
		delete(pm.peers, id)
		//fmt.Println("delete", id)
	}
}

func (pm *PeerConnManager) PeerConnNum() uint32 {
	pm.logger.Metrics(len(pm.peers))
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return uint32(len(pm.peers))
}
