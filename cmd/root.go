package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "wget",
	Short: "this is a basic wget clone made with go",
	Long:  "this is wget, a free utility for non-interactive download of files from the Web. It supports HTTP, HTTPS, and FTP protocols, as well as retrieval through HTTP proxies ans website mirroring",
	Run: func(cmd *cobra.Command, args []string) {

	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Oops. An error while executing Zero '%s'\n", err)
		os.Exit(1)
	}
}
