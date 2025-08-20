package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type Mirror_flag struct {
	Mirror  bool
	Reject  string
	Exclude string
	Convert string
}

type Download_flag struct {
	Bflag     bool
	Oflag     string
	Pflag     string
	RateLimit string
	Iflag     string
}

var Mirror Mirror_flag
var Down Download_flag
var rootCmd = &cobra.Command{
	Use:   "wget",
	Short: "this is a basic wget clone made with go",
	Long: `this is wget, a free utility for non-interactive download of files from the Web.
	It supports HTTP, HTTPS, and FTP protocols, as well as retrieval through HTTP proxies ans website mirroring`,
	Run: func(cmd *cobra.Command, args []string) {
		// the are two main tasks, the mirror part and the normal part
		// we check if mirror flag has been passed then pass the logic to another function
		// else just pars the normal flags
		if Mirror.Mirror {
			fmt.Println("the mirror flag has been passed")
		} else {
			if len(args) == 0 {
				fmt.Fprintf(os.Stderr, "no arguments were provided\n")
				os.Exit(1)
			}
			Download_file(args[0])
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
	rootCmd.Flags().BoolVarP(&Down.Bflag, "Background", "B", false, "Option for background download and logging")
	rootCmd.Flags().StringVarP(&Down.Oflag, "Output-document", "O", "", "Option for changing filename")
	rootCmd.Flags().StringVarP(&Down.Pflag, "directory-prefix", "P", "", "Option for saving a file to a specific directory")
	rootCmd.Flags().StringVarP(&Down.RateLimit, "rate-limit", "L", "", "limit your donwload speed")
	rootCmd.Flags().StringVarP(&Down.Iflag, "grouped-links", "I", "", "download from links in a txt file(concurrenlty")
	rootCmd.Flags().BoolVarP(&Mirror.Mirror, "mirror", "m", false, "mirror a webpage from link")
	rootCmd.Flags().StringVarP(&Mirror.Reject, "reject", "R", "", "list of file suffixes that the program will avoid downloading during the retrieval")
	rootCmd.Flags().StringVarP(&Mirror.Exclude, "exclude", "X", "", "Option for background download and logging")
	rootCmd.Flags().StringVarP(&Mirror.Convert, "convert-links", "c", "", "convert links in the downloaded files to be viewed offline, changing them to point to the locally downloaded resources")
}
