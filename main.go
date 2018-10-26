package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"os"

	dos "github.com/DOSNetwork/core/dosnode"
	"github.com/DOSNetwork/core/onchain"
	"github.com/DOSNetwork/core/p2p"
	"github.com/DOSNetwork/core/p2p/dht"
	"github.com/DOSNetwork/core/share/dkg/pedersen"
	"github.com/DOSNetwork/core/share/vss/pedersen"
	"github.com/DOSNetwork/core/suites"
)

type NetCofigs struct {
	NetCofigs []onchain.NetConfig
}

func readConfig() (node *onchain.NetConfig) {

	var configs NetCofigs
	// Open our jsonFile
	jsonFile, err := os.Open("./config.json")
	// if we os.Open returns an error then handle it
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Successfully Opened NetCofigs json")
	// defer the closing of our jsonFile so that we can parse it later on
	defer jsonFile.Close()

	// read our opened xmlFile as a byte array.
	byteValue, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		log.Fatal(err)
	}
	err = json.Unmarshal(byteValue, &configs)
	if err != nil {
		log.Fatal(err)
	}

	targetNode := os.Getenv("TargetNode")
	if targetNode == "" {
		fmt.Println("No TargetNode Environment variable.")
		targetNode = "rinkebyPrivateNode"
	}

	for _, config := range configs.NetCofigs {
		if targetNode == config.RemoteNodeType {
			fmt.Println("Use : ", config)
			return &config
		}
	}
	return nil
}

// main
func main() {
	roleFlag := flag.String("role", "", "BootstrapNode or not")
	nbParticipantsFlag := flag.Int("nbVerifiers", 21, "Number of Participants")
	portFlag := flag.Int("port", 0, "port number")
	bootstrapIpFlag := flag.String("bootstrapIp", "67.207.98.117:42745", "bootstrapIp")
	flag.Parse()
	role := *roleFlag
	nbParticipants := *nbParticipantsFlag
	port := *portFlag
	bootstrapIp := *bootstrapIpFlag

	config := readConfig()
	//1)Connect to Eth and Set node ID
	chainConn, err := onchain.AdaptTo(onchain.ETH, true, config)
	if err != nil {
		log.Fatal(err)
	}

	err = chainConn.UploadID()
	if err != nil {
		log.Fatal(err)
	}

	//2)Build a p2p network
	peerEvent := make(chan p2p.P2PMessage, 100)
	defer close(peerEvent)
	p, _ := p2p.CreateP2PNetwork(peerEvent, port)
	p.SetId(chainConn.GetId())
	p.Listen()

	//3)Dial to peers to build peerClient
	if role == "" {
		fmt.Println(bootstrapIp)
		p.CreatePeer(bootstrapIp, nil)
		results := p.FindNode(p.GetId(), dht.BucketSize, 20)
		for _, result := range results {
			p.GetRoutingTable().Update(result)
			fmt.Println(p.GetId().Address, "Update peer: ", result.Address)
		}
	}
	//4)Build a p2pDKG
	suite := suites.MustFind("bn256")
	peerEventForDKG := make(chan p2p.P2PMessage, 1)
	defer close(peerEventForDKG)
	p2pDkg, _ := dkg.CreateP2PDkg(p, suite, peerEventForDKG, nbParticipants)
	go p2pDkg.EventLoop()
	dkgEvent := make(chan string, 1)
	p2pDkg.SubscribeEvent(dkgEvent)
	defer close(dkgEvent)

	//5)Subscribe Event from Eth
	eventGrouping := make(chan interface{}, 1)
	defer close(eventGrouping)
	chUrl := make(chan interface{}, 20)
	defer close(chUrl)
	chRandom := make(chan interface{}, 20)
	defer close(chRandom)
	cSignatureFromPeer := make(chan vss.Signature, 100)
	defer close(cSignatureFromPeer)
	eventValidation := make(chan interface{}, 20)
	defer close(eventValidation)
	chainConn.SubscribeEvent(chUrl, onchain.SubscribeDOSProxyLogUrl)
	err = chainConn.SubscribeEvent(eventGrouping, onchain.SubscribeDOSProxyLogGrouping)
	chainConn.SubscribeEvent(chRandom, onchain.SubscribeDOSProxyLogUpdateRandom)
	chainConn.SubscribeEvent(eventValidation, onchain.SubscribeDOSProxyLogValidationResult)

	//6)Set up a dosnode pipeline
	d, _ := dos.CreateDosNode(suite, nbParticipants, p, chainConn, p2pDkg)
	d.PipeGrouping(eventGrouping)
	queryReports := d.PipeCheckURL(chUrl)
	randomReports := d.PipeGenerateRandomNumber(chRandom)
	signedReports := d.PipeSignAndBroadcast(queryReports, randomReports)
	reportsToSubmit, reportToValidate := d.PipeRecoverAndVerify(cSignatureFromPeer, signedReports)
	d.PipeSendToOnchain(reportsToSubmit)
	chRetrigerUrl := d.PipeCleanFinishMap(eventValidation, reportToValidate)

	//7)Dispatch events
	for {
		select {
		//event from peer
		case msg := <-peerEvent:
			switch content := msg.Msg.Message.(type) {
			case *vss.PublicKey:
				peerEventForDKG <- msg
			case *dkg.Deal:
				peerEventForDKG <- msg
			case *dkg.Response:
				peerEventForDKG <- msg
			case *vss.Signature:
				cSignatureFromPeer <- *content
			default:
				fmt.Println("unknown", content)
			}
		case msg := <-dkgEvent:
			if msg == "cetified" {
				gId := new(big.Int)
				gId.SetBytes(p2pDkg.GetGroupId())
				chainConn.UploadPubKey(p2pDkg.GetGroupPublicPoly().Commit())
			}
		//For retigger query
		case msg := <-chRetrigerUrl:
			chUrl <- msg
		}
	}
}
