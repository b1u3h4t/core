package main

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"time"

	"github.com/DOSNetwork/core/p2p"
	"github.com/DOSNetwork/core/share/dkg/pedersen"
	"github.com/DOSNetwork/core/suites"
	"github.com/DOSNetwork/core/testing/peerTalk/msg"

	"github.com/bshuster-repo/logrus-logstash-hook"

	"github.com/google/uuid"

	"github.com/sirupsen/logrus"
)

func main() {
	id := uuid.New()
	fmt.Println(id[:])
	log := logrus.New()
	hook, err := logrustash.NewHookWithFields("tcp", "163.172.36.173:9500", "匹凸匹test", logrus.Fields{
		"startingTimestamp": time.Now(),
		"id":                new(big.Int).SetBytes(id[:]).String(),
	})
	if err != nil {
		log.Error(err)
	}

	log.AddHook(hook)
	logger := log.WithFields(logrus.Fields{})
	p, err := p2p.CreateP2PNetwork(id[:], 44460)
	if err != nil {
		logger.Fatal(err)
	}

	if err := p.Listen(); err != nil {
		logger.Error(err)
	}

	if os.Getenv("ROLE") == "bootstrap" {
		peerEvent, err := p.SubscribeEvent(100, peerTalk.Register{}, peerTalk.Done{})
		if err != nil {
			logger.Fatal(err)
		}
		logger.Data["role"] = "bootstrap"
		logger.Info()

		peerNumStr := os.Getenv("PEERNUM")
		peerNum, err := strconv.Atoi(peerNumStr)
		if err != nil {
			logger.Fatal(err)
		}

		groupSizeStr := os.Getenv("GROUPSIZE")
		groupSize, err := strconv.Atoi(groupSizeStr)
		if err != nil {
			logger.Fatal(err)
		}

		var ids [][]byte
		idMap := make(map[string]string)
		lastAddress := p.GetIP()

		for event := range peerEvent {
			switch content := event.Msg.Message.(type) {
			case *peerTalk.Register:
				if len(ids) < peerNum {
					if err = p.SendMessage(content.GetId(), &peerTalk.Bootstrap{Address: lastAddress}); err != nil {
						logger.WithFields(logrus.Fields{
							"event": "sendMessage",
							"toId":  new(big.Int).SetBytes(content.GetId()).String(),
						}).Warn(err)
					} else {
						logger.WithFields(logrus.Fields{
							"event": "sendMessage",
							"toId":  new(big.Int).SetBytes(content.GetId()).String(),
						}).Info()
					}
					lastAddress = content.GetAddress()
					ids = append(ids, content.GetId())
					idMap[new(big.Int).SetBytes(content.GetId()).String()] = ""
					logger.WithFields(logrus.Fields{
						"event":       "receiveId",
						"id":          new(big.Int).SetBytes(content.GetId()).String(),
						"ip":          content.GetAddress(),
						"currentSize": len(ids),
						"targetSize":  peerNum,
					}).Info()
					if len(ids) == peerNum {
						logger.WithField("event", "receiveAllHello").Info()
						go func() {
							for _, id := range ids {
								fmt.Println("grouping cmd", new(big.Int).SetBytes(id).String())
								if err = p.SendMessage(id, &peerTalk.Grouping{Size: uint32(groupSize), Ids: ids}); err != nil {
									logger.WithFields(logrus.Fields{
										"event": "sendMessage",
										"toId":  new(big.Int).SetBytes(id).String(),
									}).Warn(err)
								} else {
									logger.WithFields(logrus.Fields{
										"event": "sendMessage",
										"toId":  new(big.Int).SetBytes(id).String(),
									}).Info()
								}
							}
						}()
					}
				}
			case *peerTalk.Done:
				delete(idMap, new(big.Int).SetBytes(content.GetId()).String())
				logger.WithFields(logrus.Fields{
					"event":       "receiveDone",
					"id":          new(big.Int).SetBytes(content.GetId()).String(),
					"currentSize": len(idMap),
					"targetSize":  peerNum,
				}).Info()
				if len(idMap) == 0 {
					logger.WithField("event", "allDone").Info()
					os.Exit(0)
				}
			}
		}
	} else {
		peerEvent, err := p.SubscribeEvent(100, peerTalk.Bootstrap{}, peerTalk.Grouping{})
		if err != nil {
			logger.Fatal(err)
		}
		logger.Data["role"] = "node"

		logger.Info(os.Getenv("BOOTSTRAPIP"))
		bootstrapId, err := p.ConnectTo(os.Getenv("BOOTSTRAPIP"))
		if err != nil {
			logger.Fatal(err)
		}
		suite := suites.MustFind("bn256")
		p2pdkg, err := dkg.CreateP2PDkg(p, suite)
		if err != nil {
			logger.Fatal(err)
		}

		if err = p.SendMessage(bootstrapId, &peerTalk.Register{Id: p.GetID(), Address: p.GetIP()}); err != nil {
			logger.WithFields(logrus.Fields{
				"event": "sendMessage",
				"toId":  new(big.Int).SetBytes(bootstrapId).String(),
			}).Warn(err)
		} else {
			logger.WithFields(logrus.Fields{
				"event": "sendMessage",
				"toId":  new(big.Int).SetBytes(bootstrapId).String(),
			}).Info()
		}

		for event := range peerEvent {
			switch content := event.Msg.Message.(type) {
			case *peerTalk.Bootstrap:
				if err = p.Join(content.GetAddress()); err != nil {
					logger.WithFields(logrus.Fields{
						"event":     "join",
						"bootstrap": content.GetAddress(),
					}).Warn(err)
				} else {
					logger.WithFields(logrus.Fields{
						"event":     "join",
						"bootstrap": content.GetAddress(),
					}).Info()
				}
			case *peerTalk.Grouping:
				logger.WithField("event", "receiveGrouping").Info()

				ids := content.GetIds()
				groupSize := content.GetSize()
				var group [][]byte
				for idx, id := range ids {
					if bytes.Compare(p.GetID(), id) == 0 {
						start := uint32(idx) / groupSize * groupSize
						group = ids[start : start+groupSize]
						dkgEvent, errc := p2pdkg.Start(context.Background(), group, fmt.Sprintf("%x", group))
						go func() {
							for err := range errc {
								logger.WithFields(logrus.Fields{
									"event": "dkg",
								}).Warn(err)
							}
						}()
						go func() {
							//if <-dkgEvent != nil {
							<-dkgEvent
							if err = p.SendMessage(bootstrapId, &peerTalk.Done{Id: p.GetID()}); err != nil {
								logger.WithField("event", "allDone").Error(err)
							} else {
								logger.WithField("event", "allDone").Info()
							}
							os.Exit(0)
							//}
						}()
						break
					}
				}
			}
		}
	}
}
