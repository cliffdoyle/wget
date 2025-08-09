package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	Bflag     bool
	Oflag     string
	Pflag     string
	RateLimit string
	Iflag     string
	Mirror    bool
	Reject    string
	Exclude   string
	Convert   string
)

var rootCmd = &cobra.Command{
	Use:   "wget",
	Short: "this is a basic wget clone made with go",
	Long:  "this is wget, a free utility for non-interactive download of files from the Web. It supports HTTP, HTTPS, and FTP protocols, as well as retrieval through HTTP proxies ans website mirroring",
	Run: func(cmd *cobra.Command, args []string) {
		// all our logic ,flag checking and args should start off here
		if Bflag {
			fmt.Println("we are calling our logger")
		} else {
			fmt.Println("-b flag is NOT set.")
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Oops. An error while executing Wget '%s'\n", err)
		os.Exit(1)
	}
}
func init() {
	// Normal flags (only for this command)
	rootCmd.Flags().BoolVarP(&Bflag, "Background", "B", false, "Option for background download and logging")
}
