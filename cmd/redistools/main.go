package main

import (
	"fmt"
	"github.com/rs/zerolog/log"
	clear2 "github.com/sandwich-go/redis-tools/app/clear"
	"github.com/sandwich-go/redis-tools/app/config"
	"github.com/sandwich-go/redis-tools/pkg/util"
	"github.com/spf13/cobra"
	"os"
)

var (
	rootCmd = &cobra.Command{
		Use:   "RedisTools",
		Short: "RedisTools: clear",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			log.Info().Msg(fmt.Sprintf("version: %s", util.Version()))
		},
	}
	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Version of Redis Tools",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(util.Version())
		},
	}
)

func init() {
	rootCmd.AddCommand(versionCmd)
	clear2.InitCommand(rootCmd)
}

func execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func main() {
	config.MustInitialize("./configs/redis.yaml")
	execute()
}
