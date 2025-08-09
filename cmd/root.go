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
	rootCmd.Flags().StringVarP(&Oflag, "Output-document", "O", "", "Option for changing filename")
	rootCmd.Flags().StringVarP(&Pflag, "directory-prefix", "P", "", "Option for saving a file to a specific directory")
	rootCmd.Flags().StringVarP(&RateLimit, "rate-limit", "L", "", "limit your donwload speed")
	rootCmd.Flags().StringVarP(&Iflag, "grouped-links", "I", "", "download from links in a txt file(concurrenlty")
	rootCmd.Flags().BoolVarP(&Mirror, "mirror", "m", false, "mirror a webpage from link")
	rootCmd.Flags().StringVarP(&Reject, "reject", "R", "", "list of file suffixes that the program will avoid downloading during the retrieval")
	rootCmd.Flags().StringVarP(&Exclude, "exclude", "X", "", "Option for background download and logging")
	rootCmd.Flags().StringVarP(&Convert, "convert-links", "c", "", "convert links in the downloaded files to be viewed offline, changing them to point to the locally downloaded resources")
}
