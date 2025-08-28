package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type MirrorFlags struct {
	Mirror       bool
	Reject       string
	Exclude      string
	ConvertLinks bool
	Depth        int
	Timeout      int
	MaxPages     int
}

type DownloadFlags struct {
	Bflag     bool
	Oflag     string
	Pflag     string
	RateLimit string
	Iflag     string
}

var MirrorFlagsConfig MirrorFlags
var Down DownloadFlags

var rootCmd = &cobra.Command{
	Use:   "wget",
	Short: "this is a basic wget clone made with go",
	Long: `this is wget, a free utility for non-interactive download of files from the Web.
	It supports HTTP, HTTPS, and FTP protocols, as well as retrieval through HTTP proxies ans website mirroring`,
	Run: func(cmd *cobra.Command, args []string) {
		if MirrorFlagsConfig.Mirror {
			if len(args) == 0 {
				fmt.Fprintf(os.Stderr, "Error: No URL provided for mirroring\n")
				os.Exit(1)
			}
			if err := InitMirroring(args[0]); err != nil {
				fmt.Fprintf(os.Stderr, "Mirroring failed: %v\n", err)
				os.Exit(1)
			}
		} else if Down.Iflag != "" {
			DownloadConcurrently(Down.Iflag)
		} else {
			if len(args) == 0 {
				fmt.Fprintf(os.Stderr, "Error: No URL provided\n")
				os.Exit(1)
			}
			DownloadFile(args[0])
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error executing command: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().BoolVarP(&Down.Bflag, "background", "B", false, "Download in background mode")
	rootCmd.Flags().StringVarP(&Down.Oflag, "output-document", "O", "", "Write output to specified file")
	rootCmd.Flags().StringVarP(&Down.Pflag, "directory-prefix", "P", "", "Save files to specified directory")
	rootCmd.Flags().StringVarP(&Down.RateLimit, "rate-limit", "L", "", "Limit download rate (e.g., 200k, 2M)")
	rootCmd.Flags().StringVarP(&Down.Iflag, "input-file", "i", "", "Download URLs from input file")

	rootCmd.Flags().BoolVarP(&MirrorFlagsConfig.Mirror, "mirror", "m", false, "Mirror an entire website")
	rootCmd.Flags().StringVarP(&MirrorFlagsConfig.Reject, "reject", "R", "", "Comma-separated list of file extensions to reject")
	rootCmd.Flags().StringVarP(&MirrorFlagsConfig.Exclude, "exclude-directories", "X", "", "Comma-separated list of directories to exclude")
	rootCmd.Flags().BoolVarP(&MirrorFlagsConfig.ConvertLinks, "convert-links", "k", false, "Convert links for offline viewing")
	rootCmd.Flags().IntVarP(&MirrorFlagsConfig.Depth, "level", "l", 5, "Maximum recursion depth")
	rootCmd.Flags().IntVarP(&MirrorFlagsConfig.Timeout, "mirror-timeout", "", 0, "Timeout for the mirror operation in seconds (0 = no timeout)")
	rootCmd.Flags().IntVarP(&MirrorFlagsConfig.MaxPages, "mirror-max-pages", "", 0, "Maximum number of pages to download while mirroring (0 = no limit)")
}
