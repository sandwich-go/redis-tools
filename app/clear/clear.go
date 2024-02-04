package clear

import (
	"context"
	"github.com/rs/zerolog/log"
	"github.com/sandwich-go/boost/xpanic"
	"github.com/sandwich-go/redis-tools/app"
	"github.com/sandwich-go/redis-tools/app/config"
	"github.com/spf13/cobra"
)

var (
	pattern string
	count   int64
)

var (
	rootCmd = &cobra.Command{
		Use:   "clear",
		Short: "Clear redis keys",
		Run: func(cmd *cobra.Command, args []string) {
			initialize()
		},
		SilenceErrors: true,
		SilenceUsage:  true,
	}
)

func InitCommand(parent *cobra.Command) {
	rootCmd.Flags().StringVar(&pattern, "pattern", "", "scan cursor pattern, like '_k_:*'")
	rootCmd.Flags().Int64Var(&count, "count", 100, "scan cursor count")
	parent.AddCommand(rootCmd)
}

func initialize() {
	e := app.MustNew(config.Get())
	xpanic.WhenError(e.Clear(context.Background(), pattern, count))
	log.Info().Msg("clear success")
}
