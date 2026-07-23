package internal

import (
	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install [module@version]",
	Short: "Install a module from LLAR Cloud",
	Long:  `Install obtains the selected module build from LLAR Cloud and installs it with its dependencies into the local LLAR workspace. Missing builds are produced on demand and shared for future installs.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runInstall,
}

func init() {
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	panic("TODO")
}
