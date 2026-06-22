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
	db      int
	all     bool
)

var (
	rootCmd = &cobra.Command{
		Use:   "clear",
		Short: "Clear redis keys",
		Run: func(cmd *cobra.Command, args []string) {
			switch {
			case all:
				if cmd.Flags().Changed("db") {
					log.Warn().Msg("--all is set, ignore --db")
				}
				db = app.ClearAllDB
			case !cmd.Flags().Changed("db"):
				// 未显式指定 --db 时，沿用配置文件中的 db
				db = config.Get().GetDB()
			}
			initialize()
		},
		SilenceErrors: true,
		SilenceUsage:  true,
	}
)

func InitCommand(parent *cobra.Command) {
	rootCmd.Flags().StringVar(&pattern, "pattern", "user*", "scan cursor pattern, like '_k_:*'; empty or '*' means clear all keys in scope")
	rootCmd.Flags().Int64Var(&count, "count", 100, "scan cursor count")
	rootCmd.Flags().IntVar(&db, "db", 0, "redis db to clear (default to config db when omitted); ignored in cluster mode")
	rootCmd.Flags().BoolVar(&all, "all", false, "clear all db (ignored in cluster mode)")
	parent.AddCommand(rootCmd)
}

func initialize() {
	e := app.MustNew(config.Get())
	xpanic.WhenError(e.Clear(context.Background(), db, pattern, count))
	log.Info().Msg("clear success")
}
