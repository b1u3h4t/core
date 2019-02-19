package main

import (
	"fmt"
	"github.com/DOSNetwork/core/configuration"
	"github.com/DOSNetwork/core/dosnode"
	"github.com/DOSNetwork/core/log"
	"github.com/DOSNetwork/core/onchain"
	"github.com/DOSNetwork/core/p2p"
	"github.com/DOSNetwork/core/share/dkg/pedersen"
	"github.com/DOSNetwork/core/suites"
	"github.com/urfave/cli"
	"os"
	"sort"
	"time"
)

// main

func subAction(c *cli.Context) (err error) {
	if c.NumFlags() == 0 {
		cli.ShowSubcommandHelp(c)
		return nil
	}
	return nil
}

func NewSubCommand() *cli.Command {
	return &cli.Command{Name: "subCommand",
		Usage:       "blockchain node subCommand",
		Description: "for test.",
		ArgsUsage:   "[args]",
		Flags: []cli.Flag{
			cli.IntFlag{
				Name:  "level, l",
				Usage: "log level 0-6",
				Value: -1,
			},
		},
		Action: subAction,
		OnUsageError: func(c *cli.Context, err error, isSubcommand bool) error {
			return cli.NewExitError("", 1)
		},
	}
}
func main() {
	//Read Configuration
	config := configuration.Config{}
	if err := config.LoadConfig(); err != nil {
		log.Fatal(err)
	}

	role := config.NodeRole
	port := config.Port
	bootstrapIp := config.BootStrapIp
	chainConfig := config.GetChainConfig()

	//Set up an onchain adapter
	chainConn, err := onchain.AdaptTo(config.GetCurrentType())
	if err != nil {
		log.Fatal(err)
	}

	if err = chainConn.SetAccount(config.GetCredentialPath()); err != nil {
		log.Fatal(err)
	}
	//Init log module with nodeID that is an onchain account address
	log.Init(chainConn.GetId()[:])
	if err = chainConn.Init(chainConfig); err != nil {
		log.Fatal(err)
	}

	rootCredentialPath := "testAccounts/bootCredential/fundKey"
	if err := chainConn.BalanceMaintain(rootCredentialPath); err != nil {
		log.Fatal(err)
	}

	go func() {
		fmt.Println("regular balanceMaintain started")
		ticker := time.NewTicker(time.Hour * 8)
		for range ticker.C {
			if err := chainConn.BalanceMaintain(rootCredentialPath); err != nil {
				log.Fatal(err)
			}
		}
	}()

	//Build a p2p network
	p, err := p2p.CreateP2PNetwork(chainConn.GetId(), port)
	if err != nil {
		log.Fatal(err)
	}

	if err := p.Listen(); err != nil {
		log.Fatal(err)
	}

	//Bootstrapping p2p network
	if role != "BootstrapNode" {
		if err = p.Join(bootstrapIp); err != nil {
			log.Fatal(err)
		}
	}

	//Build a p2pDKG
	suite := suites.MustFind("bn256")
	p2pDkg, err := dkg.CreateP2PDkg(p, suite)
	if err != nil {
		log.Fatal(err)
	}

	dosclient := dosnode.NewDosNode(suite, p, chainConn, p2pDkg)
	if err = dosclient.Start(); err != nil {
		log.Fatal(err)
	}

	app := cli.NewApp()
	app.Name = "DosNetwork"
	app.Version = "1.0.0"
	app.HelpName = "DosNetwork"
	app.Usage = "command line tool for Dosnetwork"
	app.UsageText = "DosNetwork [global options] command [command options] [args]"
	app.HideHelp = false
	app.HideVersion = false
	//global options
	app.Action = func(c *cli.Context) error {

		appsession := c.String("APPSESSION")
		if appsession != "" {
			fmt.Println(appsession)
		}
		appname := c.String("APPNAME")
		if appname != "" {
			fmt.Println(appname)
		}
		//todo
		return nil
	}
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "APPSESSION",
			Usage: "app version",
		},
		cli.StringFlag{
			Name:  "APPNAME",
			Usage: "APP NAME",
		},
		cli.StringFlag{
			Name:  "LOGIP",
			Usage: "",
		},
		cli.StringFlag{
			Name:  "NODEROLE",
			Usage: "",
		},
		cli.StringFlag{
			Name:  "CHAINNODE",
			Usage: "",
		},
		cli.StringFlag{
			Name:  "BOOTSTRAPIP",
			Usage: "",
		},
		cli.BoolFlag{
			Name:  "RANDOMCONNECT",
			Usage: "",
		},
	}
	//commands
	app.Commands = []cli.Command{
		*NewSubCommand(),
		//subcommand2,
	}
	sort.Sort(cli.CommandsByName(app.Commands))
	sort.Sort(cli.FlagsByName(app.Flags))

	app.Run(os.Args)

	done := make(chan interface{})
	<-done
}
